package main

import (
	"errors"
	"flag"
	"fmt"
	"log"

	"github.com/grafana/loki/pkg/sizing"
	"github.com/grafana/loki/pkg/util/flagext"
)

type Config struct {
	BytesPerSecond  flagext.ByteSize
	DaysRetention   int
	MonthlyUnitCost sizing.UnitCostInfo
}

func (c *Config) RegisterFlags(f *flag.FlagSet) {
	f.Var(&c.BytesPerSecond, "bytes-per-second", "[human readable] How many bytes per second the cluster should receive, i.e. (50MB)")
	f.IntVar(&c.DaysRetention, "days-retention", 30, "Number of days you'd like to retain logs for before deleting. For example, \"--days-retention 30\" means retain logs for 30 days before deleting.")
	f.Float64Var(&c.MonthlyUnitCost.CostPerGBMem, "monthly-cost-per-gb-mem", 2.44, "Monthly dollar cost for a megabyte of RAM")
	f.Float64Var(&c.MonthlyUnitCost.CostPerCPU, "monthly-cost-per-cpu", 18.19, "Monthly dollar cost for a CPU")
	f.Float64Var(&c.MonthlyUnitCost.CostPerGBDisk, "monthly-cost-per-gb-disk", 0.187, "Monthly dollar cost for a GB of persistent disk")
	f.Float64Var(&c.MonthlyUnitCost.CostPerGBObjStorage, "monthly-cost-per-gb-obj-storage", 0.023, "Monthly dollar cost for a GB of object storage")
}

func (c *Config) Validate() error {
	if c.BytesPerSecond <= 0 {
		return errors.New("must specify bytes-per-second")
	}

	//Is there a better way to iterate through all fields in a struct?
	if c.DaysRetention < 0 {
		return errors.New("Cannot specify negative days retention")
	}

	if c.MonthlyUnitCost.CostPerGBMem < 0 {
		return errors.New("Cannot specify negative cost per GB Mem")
	}

	if c.MonthlyUnitCost.CostPerCPU < 0 {
		return errors.New("Cannot specify negative cost per CPU")
	}

	if c.MonthlyUnitCost.CostPerGBDisk < 0 {
		return errors.New("Cannot specify negative cost per GB Disk")
	}

	if c.MonthlyUnitCost.CostPerGBObjStorage < 0 {
		return errors.New("Cannot specify negative cost per GB Object Storage")
	}

	return nil
}

func main() {
	var cfg Config
	cfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}

	cluster := sizing.SizeCluster(cfg.BytesPerSecond.Val())

	printClusterArchitecture(&cluster, &cfg, true)
}

// TODO: Add verbose flag to include the "request" (min resources) in addition to "limit" (max resources)
func printClusterArchitecture(c *sizing.ClusterResources, cfg *Config, useResourceRequests bool) {

	// loop through all components, and print out how many replicas of each component we're recommending.
	/*
		Format will look like
		"""
		Overall Requirements for a Loki cluster than can handle X volume of ingest
		Number of Nodes: 2
		Memory Required: 1000 MB
		CPUs Required: 34
		Disk Required: 100 GB

		List of all components in the Loki cluster, the number of replicas of each, and the resources required per replica

		Ingester: 5 replicas, each with:
			2000 MB RAM
			10 GB Disk
			5 CPU

		Distributor: 2 replicas, each with:
			1000 MB RAM
			1 GB Disk
			2 CPU
		"""
	*/

	totals := c.Totals()
	ingestRate := cfg.BytesPerSecond

	objectStorageRequired := sizing.ComputeObjectStorage(ingestRate, cfg.DaysRetention)
	MonthlyCosts := sizing.ComputeMonthlyCost(&cfg.MonthlyUnitCost, objectStorageRequired, totals)

	fmt.Printf("Requirements for a Loki cluster than can ingest %v per second with %d days retention\n", sizing.ReadableBytes(ingestRate), cfg.DaysRetention)
	fmt.Printf("\tNodes\n")
	fmt.Printf("\t\tMinimum count: %d\n", c.NumNodes())

	fmt.Println("\tMemory")
	fmt.Printf("\t\tMinimum: %v\n", sizing.ReadableBytes(totals.MemoryRequests))
	fmt.Printf("\t\tWith peak expected usage of: %v\n", sizing.ReadableBytes(totals.MemoryLimits))

	fmt.Println("\tCPU")
	fmt.Printf("\t\tMinimum count: %d CPUs\n", totals.CPURequests.Cores())
	fmt.Printf("\t\tWith peak expected usage of: %d CPUs\n", totals.CPULimits.Cores())

	fmt.Println("\tStorage")
	fmt.Printf("\t\t%d GB Disk\n", totals.DiskGB)
	fmt.Printf("\t\t%d TB Object Storage\n", objectStorageRequired)

	fmt.Printf("\n")

	fmt.Printf("Your expected monthly hardware costs would be approximately\n")
	fmt.Printf("\tIf you used the minimum required hardware: $%.2f\n", MonthlyCosts.BaseLoadCost)
	fmt.Printf("\tIf you used the peak required hardware: $%.2f\n\n", MonthlyCosts.PeakCost)
	fmt.Printf("This assumes a monthly cost of:\n")
	fmt.Printf("\t$%.2f per CPU\n", cfg.MonthlyUnitCost.CostPerCPU)
	fmt.Printf("\t$%.2f per GB of memory\n", cfg.MonthlyUnitCost.CostPerGBMem)
	fmt.Printf("\t$%.2f per GB of disk\n", cfg.MonthlyUnitCost.CostPerGBDisk)
	fmt.Printf("\t$%.2f per GB of object storage\n", cfg.MonthlyUnitCost.CostPerGBObjStorage)

	fmt.Printf("\n")
	fmt.Printf("List of all components in the Loki cluster, the number of replicas of each, and the resources required per replica\n")

	for _, component := range c.Components() {
		if component != nil {
			fmt.Printf("%v: %d replicas, each of which requires\n", component.Name, component.Replicas)

			fmt.Println("\tMemory")
			fmt.Printf("\t\tMinimum: %v\n", component.Resources.MemoryRequests)
			fmt.Printf("\t\tWith peak expected usage of: %v\n", component.Resources.MemoryLimits)

			fmt.Println("\tCPU")
			fmt.Printf("\t\tMinimum count: %d CPUs\n", component.Resources.CPURequests.Cores())
			fmt.Printf("\t\tWith peak expected usage of: %d CPUs\n", component.Resources.CPULimits.Cores())

			fmt.Println("\tDisk")
			fmt.Printf("\t\t%d GB\n", component.Resources.DiskGB)
		}
	}

}
