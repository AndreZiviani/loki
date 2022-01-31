package docker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	docker_types "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/relabel"
	"go.uber.org/atomic"

	"github.com/grafana/loki/clients/pkg/promtail/api"
	"github.com/grafana/loki/clients/pkg/promtail/positions"
	"github.com/grafana/loki/clients/pkg/promtail/targets/target"

	"github.com/grafana/loki/pkg/logproto"
)

type Target struct {
	logger        log.Logger
	handler       api.EntryHandler
	since         int64
	positions     positions.Positions
	containerName string
	labels        model.LabelSet
	relabelConfig []*relabel.Config
	metrics       *Metrics

	cancel  context.CancelFunc
	client  client.APIClient
	wg      sync.WaitGroup
	running *atomic.Bool
	err     error
}

func NewTarget(
	metrics *Metrics,
	logger log.Logger,
	handler api.EntryHandler,
	position positions.Positions,
	containerName string,
	labels model.LabelSet,
	relabelConfig []*relabel.Config,
	client client.APIClient,
) (*Target, error) {

	pos, err := position.Get(positions.CursorKey(containerName))
	if err != nil {
		return nil, err
	}
	var since int64
	if pos != 0 {
		since = pos
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &Target{
		logger:        logger,
		handler:       handler,
		since:         since,
		positions:     position,
		containerName: containerName,
		labels:        labels,
		relabelConfig: relabelConfig,
		metrics:       metrics,

		cancel:  cancel,
		client:  client,
		running: atomic.NewBool(false),
	}
	go t.processLoop(ctx)
	return t, nil
}

func (t *Target) processLoop(ctx context.Context) {
	t.wg.Add(1)
	defer t.wg.Done()
	t.running.Store(true)

	opts := docker_types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
		Since:      strconv.FormatInt(t.since, 10),
	}

	logs, err := t.client.ContainerLogs(ctx, t.containerName, opts)
	if err != nil {
		level.Error(t.logger).Log("msg", "could not fetch logs for container", "container", t.containerName, "err", err)
		t.err = err
		return
	}

	// Start transferring
	rstdout, wstdout := io.Pipe()
	rstderr, wstderr := io.Pipe()
	t.wg.Add(1)
	go func() {
		defer func() {
			t.wg.Done()
			wstdout.Close()
			wstderr.Close()
			t.Stop()
		}()

		written, err := stdcopy.StdCopy(wstdout, wstderr, logs)
		if err != nil {
			level.Warn(t.logger).Log("msg", "could not transfer logs", "written", written, "container", t.containerName, "err", err)
		} else {
			level.Info(t.logger).Log("msg", "finished transferring logs", "written", written, "container", t.containerName)
		}
	}()

	// Start processing
	t.wg.Add(2)
	go t.process(rstdout, "stdout")
	go t.process(rstderr, "stderr")

	// Wait until done
	<-ctx.Done()
	t.running.Store(false)
	logs.Close()
	level.Debug(t.logger).Log("msg", "done processing Docker logs", "container", t.containerName)
}

// extractTs tries for read the timestamp from the beginning of the log line.
// It's expected to follow the format 2006-01-02T15:04:05.999999999Z07:00.
func extractTs(line string) (time.Time, string, error) {
	pair := strings.SplitN(line, " ", 2)
	if len(pair) != 2 {
		return time.Now(), line, fmt.Errorf("Could not find timestamp in '%s'", line)
	}
	ts, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", pair[0])
	if err != nil {
		return time.Now(), line, fmt.Errorf("Could not parse timestamp from '%s': %w", pair[0], err)
	}
	return ts, pair[1], nil
}

func (t *Target) process(r io.Reader, logStream string) {
	defer func() {
		t.wg.Done()
	}()

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		ts, line, err := extractTs(line)
		if err != nil {
			level.Error(t.logger).Log("msg", "could not extract timestamp, skipping line", "err", err)
			t.metrics.dockerErrors.Inc()
			continue
		}

		// Add all labels from the config, relabel and filter them.
		lb := labels.NewBuilder(nil)
		for k, v := range t.labels {
			lb.Set(string(k), string(v))
		}
		lb.Set(dockerLabelLogStream, logStream)
		processed := relabel.Process(lb.Labels(), t.relabelConfig...)

		filtered := make(model.LabelSet)
		for _, lbl := range processed {
			if strings.HasPrefix(lbl.Name, "__") {
				continue
			}
			filtered[model.LabelName(lbl.Name)] = model.LabelValue(lbl.Value)
		}

		t.handler.Chan() <- api.Entry{
			Labels: filtered,
			Entry: logproto.Entry{
				Timestamp: ts,
				Line:      line,
			},
		}
		t.metrics.dockerEntries.Inc()
		t.positions.Put(positions.CursorKey(t.containerName), ts.Unix())
	}

	err := scanner.Err()
	if err != nil {
		level.Warn(t.logger).Log("msg", "finished scanning logs lines with an error", "err", err)
	}

}

func (t *Target) Stop() {
	t.cancel()
	t.wg.Wait()
	level.Debug(t.logger).Log("msg", "stopped Docker target", "container", t.containerName)
}

func (t *Target) Type() target.TargetType {
	return target.DockerTargetType
}

func (t *Target) Ready() bool {
	return t.running.Load()
}

func (t *Target) DiscoveredLabels() model.LabelSet {
	return t.labels
}

func (t *Target) Labels() model.LabelSet {
	return t.labels
}

// Details returns target-specific details.
func (t *Target) Details() interface{} {
	return map[string]string{
		"id":       t.containerName,
		"error":    t.err.Error(),
		"position": t.positions.GetString(positions.CursorKey(t.containerName)),
		"running":  strconv.FormatBool(t.running.Load()),
	}
}
