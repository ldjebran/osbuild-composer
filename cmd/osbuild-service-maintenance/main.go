package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/osbuild/osbuild-composer/internal/jobqueue/dbjobqueue"
)

func main() {
	logrus.SetReportCaller(true)

	archs := []string{"x86_64"}
	jobType := "osbuild"
	// 14 days
	cutoff := time.Now().Add(-(time.Hour * 24 * 14))
	logrus.Infof("Cutoff date: %v", cutoff)

	var conf Config
	err := LoadConfigFromEnv(&conf)
	if err != nil {
		logrus.Fatal(err)
	}

	if conf.DryRun {
		logrus.Info("Dry run, no state will be changed")
	}

	if conf.MaxConcurrentRequests == 0 {
		logrus.Fatal("Max concurrent requests is 0")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if !conf.EnableAWSMaintenance {
			logrus.Info("AWS maintenance not enabled, skipping")
			return
		}

		logrus.Info("Cleaning up AWS")
		err := AWSCleanup(conf.MaxConcurrentRequests, conf.DryRun, conf.AWSAccessKeyID, conf.AWSSecretAccessKey, "us-east-1", cutoff)
		if err != nil {
			logrus.Errorf("AWS cleanup failed: %v", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if !conf.EnableGCPMaintenance {
			logrus.Info("GCP maintenance not enabled, skipping")
			return
		}

		logrus.Info("Cleaning up GCP")
		var gcpConf GCPCredentialsConfig
		err := LoadConfigFromEnv(&gcpConf)
		if err != nil {
			logrus.Error("Unable to load GCP config from environment")
			return
		}

		if !gcpConf.valid() {
			logrus.Error("GCP credentials invalid, fields missing")
			return
		}

		creds, err := json.Marshal(&gcpConf)
		if err != nil {
			logrus.Errorf("Unable to marshal gcp conf: %v", err)
			return
		}

		err = GCPCleanup(creds, conf.MaxConcurrentRequests, conf.DryRun, cutoff)
		if err != nil {
			logrus.Errorf("GCP Cleanup failed: %v", err)
		}
	}()

	wg.Wait()
	logrus.Info("🦀🦀🦀 cloud cleanup done 🦀🦀🦀")

	if !conf.EnableDBMaintenance {
		logrus.Info("🦀🦀🦀 DB maintenance not enabled, skipping  🦀🦀🦀")
		return
	}

	dbURL := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		conf.PGUser,
		conf.PGPassword,
		conf.PGHost,
		conf.PGPort,
		conf.PGDatabase,
		conf.PGSSLMode,
	)
	jobs, err := dbjobqueue.New(dbURL)
	if err != nil {
		panic(err)
	}

	var jobTypes []string
	for _, a := range archs {
		jobTypes = append(jobTypes, fmt.Sprintf("%s:%s", jobType, a))
	}

	jobsByType, err := jobs.JobsUptoByType(jobTypes, cutoff)
	if err != nil {
		logrus.Errorf("Error querying jobs: %v", err)
		return
	}

	for k, v := range jobsByType {
		logrus.Infof("Deleting jobs and their dependencies of type %v", k)
		if conf.DryRun {
			logrus.Infof("Dry run, skipping deletion of jobs: %v", v)
			continue
		}

		for _, jobId := range v {
			err = jobs.DeleteJobIncludingDependencies(jobId)
			if err != nil {
				logrus.Errorf("Error deleting job: %v", jobId)
			}
		}
	}
	logrus.Info("🦀🦀🦀 dbqueue cleanup done 🦀🦀🦀")
}
