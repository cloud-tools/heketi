//
// Copyright (c) 2015 The heketi Authors
//
// This file is licensed to you under your choice of the GNU Lesser
// General Public License, version 3 or any later version (LGPLv3 or
// later), or the GNU General Public License, version 2 (GPLv2), in all
// cases as published by the Free Software Foundation.
//

package kubeexec

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cloud-tools/heketi/pkg/glusterfs/api"

	"github.com/cloud-tools/heketi/executors"
	"github.com/lpabon/godbc"
)

func cmdChangelogsEnabled(volName string, enabled bool) string {
	if enabled {
		return fmt.Sprintf("gluster --mode=script volume set %s changelog.changelog on", volName)
	} else {
		return fmt.Sprintf("gluster --mode=script volume set %s changelog.changelog off", volName)
	}
}

// function is used to determine if error message for some action implies that action is already executed
func errAlreadyInTargetState(err error, action api.GeoReplicationActionType) bool {
	errMsg := strings.ToLower(err.Error())
	if action == api.GeoReplicationActionCreate && strings.Contains(errMsg, "is already created") {
		return true
	} else if action == api.GeoReplicationActionStart && strings.Contains(errMsg, "already started") {
		return true
	} else if action == api.GeoReplicationActionStop && strings.Contains(errMsg, "not running on this node") {
		return true
	} else if action == api.GeoReplicationActionPause && strings.Contains(errMsg, "already paused") {
		return true
	} else if action == api.GeoReplicationActionResume && strings.Contains(errMsg, "is not paused") {
		return true
	} else if action == api.GeoReplicationActionDelete && strings.Contains(errMsg, "does not exist") {
		return true
	} else {
		return false
	}
}

// GeoReplicationCreate creates a geo-rep session for the given volume
func (s *KubeExecutor) GeoReplicationCreate(host, volume string, geoRep *executors.GeoReplicationRequest) error {
	logger.Debug("In GeoReplicationCreate")
	logger.Debug("actionParams: %+v", geoRep.ActionParams)

	godbc.Require(host != "")
	godbc.Require(volume != "")
	godbc.Require(geoRep.SlaveHost != "")
	godbc.Require(geoRep.SlaveVolume != "")
	_, optionOK := geoRep.ActionParams["option"]
	godbc.Require(optionOK && (geoRep.ActionParams["option"] == "push-pem" || geoRep.ActionParams["option"] == "no-verify"))

	sshPort := " "
	if geoRep.SlaveSSHPort != 0 {
		sshPort = fmt.Sprintf(" ssh-port %d ", geoRep.SlaveSSHPort)
	}
	cmd := fmt.Sprintf("gluster --mode=script volume geo-replication %s %s::%s create%s%s", volume, geoRep.SlaveHost, geoRep.SlaveVolume, sshPort, geoRep.ActionParams["option"])

	if force, ok := geoRep.ActionParams["force"]; ok && force == "true" {
		cmd = fmt.Sprintf("%s %s", cmd, "force")
	}

	// create session and then make volume read-only
	commands := []string{cmd, cmdChangelogsEnabled(volume, false)}
	for _, command := range commands {
		for i := 0; ; i++ {
			if _, err := s.RemoteExecutor.RemoteCommandExecute(host, []string{command}, 10); err != nil {
				if errAlreadyInTargetState(err, api.GeoReplicationActionCreate) {
					logger.Debug("Action %s already performed for volume %s", api.GeoReplicationActionCreate, volume)
					break
				} else if i >= 50 {
					return err
				}
				time.Sleep(3 * time.Second)
			} else {
				break
			}
		}
	}
	return nil
}

// GeoReplicationAction executes the given geo-replication action for the given volume
func (s *KubeExecutor) GeoReplicationAction(host, volume, action string, geoRep *executors.GeoReplicationRequest) error {
	logger.Debug("In GeoReplicationAction: %s", action)

	godbc.Require(host != "")
	godbc.Require(volume != "")
	godbc.Require(geoRep.SlaveHost != "")
	godbc.Require(geoRep.SlaveVolume != "")

	cmd := fmt.Sprintf("gluster --mode=script volume geo-replication %s %s::%s %s", volume, geoRep.SlaveHost, geoRep.SlaveVolume, action)

	if force, ok := geoRep.ActionParams["force"]; ok && force == "true" {
		cmd = fmt.Sprintf("%s %s", cmd, force)
	}

	commands := []string{cmd}
	apiAction := api.GeoReplicationActionType(action)
	if apiAction == api.GeoReplicationActionStart {
		commands = append(commands, cmdChangelogsEnabled(volume, true))
	} else if apiAction == api.GeoReplicationActionStop {
		commands = append(commands, cmdChangelogsEnabled(volume, false))
	}

	for _, command := range commands {
		for i := 0; ; i++ {
			if _, err := s.RemoteExecutor.RemoteCommandExecute(host, []string{command}, 10); err != nil {
				if errAlreadyInTargetState(err, api.GeoReplicationActionType(action)) {
					logger.Debug("Action %s already performed for volume %s", action, volume)
					break
				} else if i >= 50 {
					return err
				}
				time.Sleep(3 * time.Second)
			} else {
				break
			}
		}
	}
	return nil
}

// GeoReplicationStatus returns the geo-replication status
func (s *KubeExecutor) GeoReplicationStatus(host string) (*executors.GeoReplicationStatus, error) {
	logger.Debug("In GeoReplicationStatus")

	godbc.Require(host != "")

	type CliOutput struct {
		OpRet        int                            `xml:"opRet"`
		OpErrno      int                            `xml:"opErrno"`
		OpErrStr     string                         `xml:"opErrstr"`
		GeoRepStatus executors.GeoReplicationStatus `xml:"geoRep"`
	}

	commands := []string{"gluster --mode=script volume geo-replication status --xml"}

	var output []string
	var err error
	if output, err = s.RemoteExecutor.RemoteCommandExecute(host, commands, 10); err != nil {
		return nil, err
	}

	var geoRepStatus CliOutput

	if err := xml.Unmarshal([]byte(output[0]), &geoRepStatus); err != nil {
		return nil, fmt.Errorf("Unable to determine geo-replication status on host %s: %v", host, err)
	}

	return &geoRepStatus.GeoRepStatus, nil
}

// GeoReplicationVolumeStatus returns the geo-replication status of a specific volume
func (s *KubeExecutor) GeoReplicationVolumeStatus(host, volume string) (*executors.GeoReplicationStatus, error) {
	logger.Debug("In GeoReplicationVolumeStatus")

	godbc.Require(host != "")
	godbc.Require(volume != "")

	type CliOutput struct {
		OpRet        int                            `xml:"opRet"`
		OpErrno      int                            `xml:"opErrno"`
		OpErrStr     string                         `xml:"opErrstr"`
		GeoRepStatus executors.GeoReplicationStatus `xml:"geoRep"`
	}

	cmd := fmt.Sprintf("gluster --mode=script volume geo-replication %s status --xml", volume)
	commands := []string{cmd}

	var output []string
	var err error
	if output, err = s.RemoteExecutor.RemoteCommandExecute(host, commands, 10); err != nil {
		return nil, err
	}

	var geoRepStatus CliOutput

	if err := xml.Unmarshal([]byte(output[0]), &geoRepStatus); err != nil {
		return nil, fmt.Errorf("Unable to determine geo-replication status for volume %v: %v", volume, err)
	}

	return &geoRepStatus.GeoRepStatus, nil
}

// GeoReplicationConfig configures the geo-replication session for the given volume
func (s *KubeExecutor) GeoReplicationConfig(host, volume string, geoRep *executors.GeoReplicationRequest) error {
	logger.Debug("In GeoReplicationConfig")

	godbc.Require(host != "")
	godbc.Require(volume != "")
	godbc.Require(geoRep.SlaveHost != "")
	godbc.Require(geoRep.SlaveVolume != "")

	commands := s.createConfigCommands(volume, geoRep)

	if _, err := s.RemoteExecutor.RemoteCommandExecute(host, commands, 10); err != nil {
		logger.LogError("Invalid configuration for volume georeplication %s", volume)
		return err
	}
	return nil
}

func (s *KubeExecutor) createConfigCommands(volume string, geoRep *executors.GeoReplicationRequest) []string {
	commands := []string{}

	cmdTpl := "gluster --mode=script volume geo-replication %s %s::%s config %s %s"
	for param, value := range geoRep.ActionParams {
		switch param {
		// String parameters
		case "log-level", "gluster-log-level", "changelog-log-level", "ssh-command", "rsync-command":
			commands = append(commands, fmt.Sprintf(cmdTpl, volume, geoRep.SlaveHost, geoRep.SlaveVolume, param, value))
		// Boolean parameters
		case "use-tarssh", "use-meta-volume":
			if value != "false" && value != "true" {
				logger.LogError("Invalid value %v for config option %s", value, param)
				continue
			}
			commands = append(commands, fmt.Sprintf(cmdTpl, volume, geoRep.SlaveHost, geoRep.SlaveVolume, param, value))
		case "ignore-deletes":
			if value != "false" && value != "true" {
				logger.LogError("Invalid value %v for config option %s", value, param)
				continue
			}

			// set to 1 if explicitly set to true, skip otherwise
			if value == "true" {
				commands = append(commands, fmt.Sprintf(cmdTpl, volume, geoRep.SlaveHost, geoRep.SlaveVolume, param, "1"))
			}
		// Integer parameters
		case "timeout", "sync-jobs":
			if _, err := strconv.Atoi(value); err != nil {
				logger.LogError("Invalid value %v for config option %s", value, param)
				continue
			}
			commands = append(commands, fmt.Sprintf(cmdTpl, volume, geoRep.SlaveHost, geoRep.SlaveVolume, param, value))
		case "ssh-port":
			// due to gluster cli client inconsistency, set the parameter to ssh_port
			param = "ssh_port"
			if _, err := strconv.Atoi(value); err != nil {
				logger.LogError("Invalid value %v for config option %s", value, param)
				continue
			}
			commands = append(commands, fmt.Sprintf(cmdTpl, volume, geoRep.SlaveHost, geoRep.SlaveVolume, param, value))
		}

	}

	return commands
}
