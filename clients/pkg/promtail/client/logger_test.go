package client

import (
	"net/url"
	"testing"
	"time"

	cortexflag "github.com/grafana/dskit/flagext"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/clients/pkg/promtail/api"

	"github.com/grafana/loki/pkg/logproto"
	"github.com/grafana/loki/pkg/logutil"
	"github.com/grafana/loki/pkg/util/flagext"
)

func TestNewLogger(t *testing.T) {
	_, err := NewLogger(nil, logutil.Logger, flagext.LabelSet{}, []Config{}...)
	require.Error(t, err)

	l, err := NewLogger(nil, logutil.Logger, flagext.LabelSet{}, []Config{{URL: cortexflag.URLValue{URL: &url.URL{Host: "string"}}}}...)
	require.NoError(t, err)
	l.Chan() <- api.Entry{Labels: model.LabelSet{"foo": "bar"}, Entry: logproto.Entry{Timestamp: time.Now(), Line: "entry"}}
	l.Stop()
}
