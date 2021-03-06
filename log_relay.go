package main

import (
	"bufio"
	"io"

	"github.com/Nitro/sidecar-executor/container"
	"github.com/Nitro/sidecar-executor/loghooks"
	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
)

func (exec *sidecarExecutor) configureLogRelay(containerId string,
	labels map[string]string, output io.Writer) *logrus.Entry {

	syslogger := log.New()
	// We relay UDP syslog because we don't plan to ship it off the box
	// and because it's simplest since there is no backpressure issue to
	// deal with.
	hook, err := loghooks.NewUDPHook(exec.config.SyslogAddr)
	if err != nil {
		log.Fatalf("Error adding hook: %s", err)
	}

	syslogger.Hooks.Add(hook)
	syslogger.SetFormatter(&logrus.JSONFormatter{
		FieldMap: logrus.FieldMap{
			log.FieldKeyTime:  "Timestamp",
			log.FieldKeyLevel: "Level",
			log.FieldKeyMsg:   "Payload",
			log.FieldKeyFunc:  "Func",
		},
	})
	syslogger.SetOutput(output)

	// Loop through the fields we're supposed to pass, and add them from the
	// Docker labels on this container
	fields := make(log.Fields, len(exec.config.SendDockerLabels))
	for _, field := range exec.config.SendDockerLabels {
		if val, ok := labels[field]; ok {
			fields[field] = val
		}
	}

	return syslogger.WithFields(fields)
}

// relayLogs will watch a container and send the logs to Syslog
func (exec *sidecarExecutor) relayLogs(quitChan chan struct{},
	containerId string, labels map[string]string, output io.Writer) {

	logger := exec.configureLogRelay(containerId, labels, output)

	logger.Infof("sidecar-executor starting log pump for '%s'", containerId[:12])
	log.Info("Started syslog log pump") // Send to local log output

	outrd, outwr := io.Pipe()
	errrd, errwr := io.Pipe()

	// Tell Docker client to start pumping logs into our pipes
	container.FollowLogs(exec.client, containerId, 0, outwr, errwr)

	go exec.handleOneStream(quitChan, "stdout", logger, outrd)
	go exec.handleOneStream(quitChan, "stderr", logger, errrd)

	<-quitChan
}

// handleOneStream will process one data stream into logs
func (exec *sidecarExecutor) handleOneStream(quitChan chan struct{}, name string,
	logger *log.Entry, in io.Reader) {

	scanner := bufio.NewScanner(in) // Defaults to splitting as lines

	for scanner.Scan() {
		text := scanner.Text()
		log.Debugf("docker: %s", text)

		switch name {
		case "stdout":
			logger.Info(text) // Send to syslog "info"
		case "stderr":
			logger.Error(text) // Send to syslog "error"
		default:
			log.Errorf("handleOneStream(): Unknown stream type '%s'. Exiting log pump.", name)
			return
		}

		select {
		case <-quitChan:
			return
		default:
			// nothing
		}
	}
	if err := scanner.Err(); err != nil {
		log.Errorf("handleOneStream() error reading Docker log input: '%s'. Exiting log pump '%s'.", err, name)
	}

	log.Warnf("Log pump exited for '%s'", name)
}
