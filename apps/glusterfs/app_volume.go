//
// Copyright (c) 2015 The heketi Authors
//
// This file is licensed to you under your choice of the GNU Lesser
// General Public License, version 3 or any later version (LGPLv3 or
// later), or the GNU General Public License, version 2 (GPLv2), in all
// cases as published by the Free Software Foundation.
//

package glusterfs

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/boltdb/bolt"
	"github.com/cloud-tools/heketi/pkg/db"
	"github.com/cloud-tools/heketi/pkg/glusterfs/api"
	"github.com/cloud-tools/heketi/pkg/utils"
	"github.com/gorilla/mux"
)

const (
	VOLUME_CREATE_MAX_SNAPSHOT_FACTOR = 100
)

func (a *App) VolumeCreate(w http.ResponseWriter, r *http.Request) {

	var msg api.VolumeCreateRequest
	err := utils.GetJsonFromRequest(r, &msg)
	if err != nil {
		http.Error(w, "request unable to be parsed", 422)
		return
	}
	err = msg.Validate()
	if err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		logger.LogError("validation failed: " + err.Error())
		return
	}

	switch {
	case msg.Gid < 0:
		http.Error(w, "Bad group id less than zero", http.StatusBadRequest)
		logger.LogError("Bad group id less than zero")
		return
	case msg.Gid >= math.MaxInt32:
		http.Error(w, "Bad group id equal or greater than 2**32", http.StatusBadRequest)
		logger.LogError("Bad group id equal or greater than 2**32")
		return
	}

	switch msg.Durability.Type {
	case api.DurabilityEC:
	case api.DurabilityReplicate:
	case api.DurabilityDistributeOnly:
	case "":
		msg.Durability.Type = api.DurabilityDistributeOnly
	default:
		http.Error(w, "Unknown durability type", http.StatusBadRequest)
		logger.LogError("Unknown durability type")
		return
	}

	if msg.Size < 1 {
		http.Error(w, "Invalid volume size", http.StatusBadRequest)
		logger.LogError("Invalid volume size")
		return
	}
	if msg.Snapshot.Enable {
		if msg.Snapshot.Factor < 1 || msg.Snapshot.Factor > VOLUME_CREATE_MAX_SNAPSHOT_FACTOR {
			http.Error(w, "Invalid snapshot factor", http.StatusBadRequest)
			logger.LogError("Invalid snapshot factor")
			return
		}
	}

	if msg.Durability.Type == api.DurabilityReplicate {
		if msg.Durability.Replicate.Replica > 3 {
			http.Error(w, "Invalid replica value", http.StatusBadRequest)
			logger.LogError("Invalid replica value")
			return
		}
	}

	if msg.Durability.Type == api.DurabilityEC {
		d := msg.Durability.Disperse
		// Place here correct combinations
		switch {
		case d.Data == 2 && d.Redundancy == 1:
		case d.Data == 4 && d.Redundancy == 2:
		case d.Data == 8 && d.Redundancy == 3:
		case d.Data == 8 && d.Redundancy == 4:
		default:
			http.Error(w,
				fmt.Sprintf("Invalid dispersion combination: %v+%v", d.Data, d.Redundancy),
				http.StatusBadRequest)
			logger.LogError(fmt.Sprintf("Invalid dispersion combination: %v+%v", d.Data, d.Redundancy))
			return
		}
	}

	// Check that the clusters requested are available
	err = a.db.View(func(tx *bolt.Tx) error {
		// :TODO: All we need to do is check for one instead of gathering all keys
		clusters, err := ClusterList(tx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}
		if len(clusters) == 0 {
			http.Error(w, fmt.Sprintf("No clusters configured"), http.StatusBadRequest)
			logger.LogError("No clusters configured")
			return ErrNotFound
		}
		// check if no defined clusters, select one of master or slave if they defined
		// Check the clusters requested are correct
		for _, clusterid := range msg.Clusters {
			_, err := NewClusterEntryFromId(tx, clusterid)
			if err != nil {
				http.Error(w, fmt.Sprintf("Cluster id %v not found", clusterid), http.StatusBadRequest)
				logger.LogError(fmt.Sprintf("Cluster id %v not found", clusterid))
				return err
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	// if no clusters defined then get master and slave, and create 2 volumes
	// with geo-replication on it
	// 2do: support 2+ clusters, now only 2 supported
	MasterCluster := []string{}
	SlaveCluster := []string{}

	if len(msg.Clusters) == 0 {
		MasterCluster, SlaveCluster = a.MasterSlaveClustersCheck()
		logger.Debug("Master Cluster found as %v and Slave Cluster found as %v \n", MasterCluster, SlaveCluster)
	}

	vol := NewVolumeEntryFromRequest(&msg)

	if uint64(msg.Size)*GB < vol.Durability.MinVolumeSize() {
		http.Error(w, fmt.Sprintf("Requested volume size (%v GB) is "+
			"smaller than the minimum supported volume size (%v)",
			msg.Size, vol.Durability.MinVolumeSize()),
			http.StatusBadRequest)
		logger.LogError(fmt.Sprintf("Requested volume size (%v GB) is "+
			"smaller than the minimum supported volume size (%v)",
			msg.Size, vol.Durability.MinVolumeSize()))
		return
	}

	if len(MasterCluster) != 0 {
		remvol := NewVolumeEntryFromRequest(&msg)

		vol.Info.Remvolid = remvol.Info.Id
		vol.Info.Clusters = MasterCluster
		remvol.Info.Remvolid = vol.Info.Id
		remvol.Info.Clusters = SlaveCluster
		logger.Debug("For volume %v set clusters \n", vol.Info.Id, vol.Info.Clusters)
		logger.Debug("For remote volume %v set clusters %v \n", remvol.Info.Id, remvol.Info.Clusters)

		remvc := NewVolumeCreateOperation(remvol, a.db)
		if a.conf.RetryLimits.VolumeCreate > 0 {
			remvc.maxRetries = a.conf.RetryLimits.VolumeCreate
		}

		vc := NewVolumeCreateOperation(vol, a.db)
		if a.conf.RetryLimits.VolumeCreate > 0 {
			vc.maxRetries = a.conf.RetryLimits.VolumeCreate
		}

		logger.Info("Creating remote volume %v", remvol.Info.Id)
		remvcerr := remvol.Create(a.db, a.executor)
		if remvcerr != nil {
			logger.LogError("Failed to create volume: %v", err)
			return
		}
		logger.Info("Created remote volume %v", remvol.Info.Id)

		if err := AsyncHttpOperation(a, w, r, vc); err != nil {
			OperationHttpErrorf(w, err, "Failed to allocate new volume: %v", err)
			logger.LogError("Failed to allocate new volume: %v", err)
			return
		}

		masterSshCluster := strings.Join(MasterCluster, ",")

		logger.Debug("For Vol %v Selected host %v from hosts %v", vol.Info.Id, vol.Info.Mount.GlusterFS.Hosts[0], vol.Info.Mount.GlusterFS.Hosts)
		logger.Debug("For Remvol %v Selected host %v from hosts %v", remvol.Info.Id, remvol.Info.Mount.GlusterFS.Hosts[0], remvol.Info.Mount.GlusterFS.Hosts)
		// check volumes created and create georep sessions
		vcerr := a.db.View(func(tx *bolt.Tx) error {
			for i := 0; ; i++ {
				if volumeInfo, err := volumeInfo(tx, vol.Info.Id); err != nil {
					if i >= 100 {
						return err
					}
					time.Sleep(3 * time.Second)
				} else {
					// Create Slave-master geo session without start for switdhower needs
					logger.Debug("Remote Volume is %v", volumeInfo)

					AsyncHttpRedirectFunc(a, w, r, func() (string, error) {
						// start sshd on master to init georep session
						sshonerr := a.MasterSlaveSshdSet("start", masterSshCluster)
						if sshonerr != nil {
							logger.LogError("Error during stop ssh : %v \n", sshonerr)
						}

						actionParams := make(map[string]string)
						actionParams["option"] = "push-pem"
						actionParams["force"] = "true"

						geoRepCreateRequest := api.GeoReplicationRequest{
							Action:       api.GeoReplicationActionCreate,
							ActionParams: actionParams,
							GeoReplicationInfo: api.GeoReplicationInfo{
								SlaveHost:    vol.Info.Mount.GlusterFS.Hosts[0],
								SlaveVolume:  vol.Info.Name,
								SlaveSSHPort: 2222,
							},
						}

						id := remvol.Info.Id
						var masterVolume *VolumeEntry
						var host string

						err = a.db.View(func(tx *bolt.Tx) error {
							masterVolume, err = NewVolumeEntryFromId(tx, id)
							logger.Debug("For volume geo %v with id %v geo \n", masterVolume, masterVolume.Info.Id)

							if err == ErrNotFound {
								logger.LogError("[ERROR] Volume Id not found: %v \n", err)
								return err
							} else if err != nil {
								logger.LogError("[ERROR] Internal error: %v \n", err)
								return err
							}

							cluster, err := NewClusterEntryFromId(tx, masterVolume.Info.Cluster)
							if err == ErrNotFound {
								return err
							} else if err != nil {
								return err
							}

							node, err := NewNodeEntryFromId(tx, cluster.Info.Nodes[0])
							if err == ErrNotFound {
								logger.LogError("[ERROR] Node Id not found: %v", err)
								return err
							} else if err != nil {
								logger.LogError("[ERROR] Internal error: %v", err)
								return err
							}

							host = node.ManageHostName()

							return nil
						})

						if err != nil {
							logger.LogError("Error during found master volume : %v \n", err)
							return "", err
						}

						logger.Debug("Creating geo replicate with request %v \n", geoRepCreateRequest)
						if err := masterVolume.GeoReplicationAction(a.db, a.executor, host, geoRepCreateRequest); err != nil {
							return "", err
						}

						return "/volumes/" + masterVolume.Info.Id + "/georeplication", nil
					})

					// Creater master-slave session
					AsyncHttpRedirectFunc(a, w, r, func() (string, error) {

						actionParams := make(map[string]string)
						actionParams["option"] = "push-pem"
						actionParams["force"] = "true"

						geoRepCreateRequest := api.GeoReplicationRequest{
							Action:       api.GeoReplicationActionCreate,
							ActionParams: actionParams,
							GeoReplicationInfo: api.GeoReplicationInfo{
								SlaveHost:    remvol.Info.Mount.GlusterFS.Hosts[0],
								SlaveVolume:  remvol.Info.Name,
								SlaveSSHPort: 2222,
							},
						}

						id := vol.Info.Id
						var masterVolume *VolumeEntry
						var host string

						err = a.db.View(func(tx *bolt.Tx) error {
							masterVolume, err = NewVolumeEntryFromId(tx, id)
							logger.Debug("For volume geo %v with id %v geo \n", masterVolume, masterVolume.Info.Id)

							if err == ErrNotFound {
								logger.LogError("[ERROR] Volume Id not found: %v \n", err)
								http.Error(w, "Volume Id not found", http.StatusNotFound)
								return err
							} else if err != nil {
								logger.LogError("[ERROR] Internal error: %v \n", err)
								http.Error(w, err.Error(), http.StatusInternalServerError)
								return err
							}

							cluster, err := NewClusterEntryFromId(tx, masterVolume.Info.Cluster)
							if err == ErrNotFound {
								http.Error(w, "Cluster Id not found", http.StatusNotFound)
								return err
							} else if err != nil {
								http.Error(w, err.Error(), http.StatusInternalServerError)
								return err
							}

							node, err := NewNodeEntryFromId(tx, cluster.Info.Nodes[0])
							if err == ErrNotFound {
								logger.LogError("[ERROR] Node Id not found: %v", err)
								http.Error(w, "Node Id not found", http.StatusNotFound)
								return err
							} else if err != nil {
								logger.LogError("[ERROR] Internal error: %v", err)
								http.Error(w, err.Error(), http.StatusInternalServerError)
								return err
							}

							host = node.ManageHostName()

							return nil
						})

						if err != nil {
							logger.LogError("[ERROR] Error during found master volume : %v \n", err)
							return "", err
						}

						logger.Debug("Create geo replicate with request %v \n", geoRepCreateRequest)
						if err := masterVolume.GeoReplicationAction(a.db, a.executor, host, geoRepCreateRequest); err != nil {
							return "", err
						}

						geoRepStartRequest := api.GeoReplicationRequest{
							Action: api.GeoReplicationActionStart,
							GeoReplicationInfo: api.GeoReplicationInfo{
								SlaveHost:   remvol.Info.Mount.GlusterFS.Hosts[0],
								SlaveVolume: remvol.Info.Name,
							},
						}

						logger.Debug("Start geo replicate with request %v \n", geoRepStartRequest)

						if err := masterVolume.GeoReplicationAction(a.db, a.executor, host, geoRepStartRequest); err != nil {
							return "", err
						}

						logger.Info("Geo-Replication is started for volume: %v \n", masterVolume)

						// 2do : check if rem georep created
						time.Sleep(10 * time.Second)

						return "/volumes/" + masterVolume.Info.Id + "/georeplication", nil

					})
					break
				}
			}
			return nil
		})
		if vcerr != nil {
			logger.LogError("Error during search remvol: %v \n", vcerr)
			return
		}
	} else {
		// non - georep logic
		vc := NewVolumeCreateOperation(vol, a.db)
		if a.conf.RetryLimits.VolumeCreate > 0 {
			vc.maxRetries = a.conf.RetryLimits.VolumeCreate
		}

		if err := AsyncHttpOperation(a, w, r, vc); err != nil {
			OperationHttpErrorf(w, err, "Failed to allocate new volume: %v", err)
			return
		}
	}

}

func (a *App) VolumeList(w http.ResponseWriter, r *http.Request) {

	var list api.VolumeListResponse

	// Get all the cluster ids from the DB
	err := a.db.View(func(tx *bolt.Tx) error {
		var err error

		list.Volumes, err = ListCompleteVolumes(tx)
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		logger.Err(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Send list back
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(list); err != nil {
		panic(err)
	}
}

func (a *App) VolumeInfo(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	var info *api.VolumeInfoResponse
	err := a.db.View(func(tx *bolt.Tx) error {
		entry, err := NewVolumeEntryFromId(tx, id)
		if err == ErrNotFound || !entry.Visible() {
			// treat an invisible entry like it doesn't exist
			http.Error(w, "Id not found", http.StatusNotFound)
			return ErrNotFound
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		info, err = entry.NewInfoResponse(tx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}
		err = UpdateVolumeInfoComplete(tx, info)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		return nil
	})
	if err != nil {
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(info); err != nil {
		panic(err)
	}

}

func (a *App) VolumeDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	logger.Info("Request to delete volume with id %v", id)

	masterClustersList, slaveClustersList := a.MasterSlaveClustersCheck()
	if len(masterClustersList) == 0 || len(slaveClustersList) == 0 {
		err := logger.LogError("Either master or slave cluster not found")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var masterVolume *VolumeEntry
	var masterNode *NodeEntry
	var slaveVolume *VolumeEntry
	var slaveNode *NodeEntry
	err := a.db.View(func(tx *bolt.Tx) error {
		var err error
		masterVolume, err = NewVolumeEntryFromId(tx, id)
		if err == ErrNotFound {
			err = logger.LogError("Master volume with id %v not found \n", id)
			http.Error(w, err.Error(), http.StatusNotFound)
			return err
		} else if err != nil {
			err = logger.LogError("Error finding master volume with id %v: %s", id, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		slaveVolume, err = NewVolumeEntryFromId(tx, masterVolume.Info.Remvolid)
		if err == ErrNotFound {
			err = logger.LogError("Slave volume with id %v not found \n", masterVolume.Info.Remvolid)
			http.Error(w, err.Error(), http.StatusNotFound)
			return err
		} else if err != nil {
			err = logger.LogError("Error finding slave volume with id %v: %s", masterVolume.Info.Remvolid, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		masterCluster, err := NewClusterEntryFromId(tx, masterClustersList[0])
		if err != nil {
			err = logger.LogError("Error finding master cluster with id %v: %s", masterClustersList[0], err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}
		slaveCluster, err := NewClusterEntryFromId(tx, slaveClustersList[0])
		if err != nil {
			err = logger.LogError("Error finding slave cluster with id %v: %s", slaveClustersList[0], err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		if masterNode, err = NewNodeEntryFromId(tx, masterCluster.Info.Nodes[0]); err != nil {
			err = logger.LogError("Error finding master node with id %v: %s", masterCluster.Info.Nodes[0], err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}
		if slaveNode, err = NewNodeEntryFromId(tx, slaveCluster.Info.Nodes[0]); err != nil {
			err = logger.LogError("Error finding slave node with id %v: %s", slaveCluster.Info.Nodes[0], err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		if masterVolume.Info.Name == db.HeketiStorageVolumeName {
			err := fmt.Errorf("Cannot delete volume containing the Heketi database")
			http.Error(w, err.Error(), http.StatusConflict)
			return err
		}

		return nil
	})
	if err != nil {
		return
	}

	if err := asyncVolumeDelete(w, r, a, masterVolume, slaveVolume, masterNode); err != nil {
		err = logger.LogError("Failed to asynchronously delete master volume %v: %v", masterVolume.Info.Name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := asyncVolumeDelete(w, r, a, slaveVolume, masterVolume, slaveNode); err != nil {
		err = logger.LogError("Failed to asynchronously delete slave volume %v: %v", slaveVolume.Info.Name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (a *App) VolumeExpand(w http.ResponseWriter, r *http.Request) {
	logger.Debug("In VolumeExpand")

	vars := mux.Vars(r)
	id := vars["id"]

	var msg api.VolumeExpandRequest
	err := utils.GetJsonFromRequest(r, &msg)
	if err != nil {
		http.Error(w, "request unable to be parsed", 422)
		return
	}
	logger.Debug("Msg: %v", msg)
	err = msg.Validate()
	if err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		logger.LogError("validation failed: " + err.Error())
		return
	}

	if msg.Size < 1 {
		http.Error(w, "Invalid volume size", http.StatusBadRequest)
		return
	}
	logger.Debug("Size: %v", msg.Size)

	var volume *VolumeEntry
	err = a.db.View(func(tx *bolt.Tx) error {

		var err error
		volume, err = NewVolumeEntryFromId(tx, id)
		if err == ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
			return err
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		return nil

	})
	if err != nil {
		return
	}

	ve := NewVolumeExpandOperation(volume, a.db, msg.Size)
	if err := AsyncHttpOperation(a, w, r, ve); err != nil {
		OperationHttpErrorf(w, err, "Failed to allocate volume expansion: %v", err)
		return
	}
}

func (a *App) VolumeClone(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	vol_id := vars["id"]

	var msg api.VolumeCloneRequest
	err := utils.GetJsonFromRequest(r, &msg)
	if err != nil {
		http.Error(w, "request unable to be parsed", http.StatusUnprocessableEntity)
		return
	}
	err = msg.Validate()
	if err != nil {
		http.Error(w, "validation failed: "+err.Error(),
			http.StatusBadRequest)
		logger.LogError("validation failed: " + err.Error())
		return
	}

	var volume *VolumeEntry
	err = a.db.View(func(tx *bolt.Tx) error {
		var err error // needed otherwise 'volume' will be nil after View()
		volume, err = NewVolumeEntryFromId(tx, vol_id)
		if err == ErrNotFound || !volume.Visible() {
			// treat an invisible volume like it doesn't exist
			http.Error(w, err.Error(), http.StatusNotFound)
			return err
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		return nil
	})
	if err != nil {
		return
	}

	op := NewVolumeCloneOperation(volume, a.db, msg.Name)
	if err := AsyncHttpOperation(a, w, r, op); err != nil {
		OperationHttpErrorf(w, err,
			"Failed clone volume %v: %v", vol_id, err)
		return
	}
}

func (a *App) VolumeSetBlockRestriction(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	var volume *VolumeEntry
	// Unmarshal JSON
	var msg api.VolumeBlockRestrictionRequest
	err := utils.GetJsonFromRequest(r, &msg)
	if err != nil {
		http.Error(w, "request unable to be parsed", 422)
		return
	}
	err = msg.Validate()
	if err != nil {
		http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
		logger.LogError("validation failed: " + err.Error())
		return
	}

	// Check for valid id, return immediately if not valid
	err = a.db.View(func(tx *bolt.Tx) error {
		volume, err = NewVolumeEntryFromId(tx, id)
		if err == ErrNotFound || !volume.Visible() {
			// treat an invisible volume like it doesn't exist
			http.Error(w, err.Error(), http.StatusNotFound)
			return err
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}

		return nil
	})
	if err != nil {
		return
	}

	vsbro := NewVolumeSetBlockRestrictionOperation(volume, a.db, msg.Restriction)
	if err := AsyncHttpOperation(a, w, r, vsbro); err != nil {
		OperationHttpErrorf(w, err, "Failed to set block restriction: %v", err)
		return
	}
}

func (a *App) VolumeSnapshot(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	var snapshotRequest api.VolumeSnapshotRequest
	err := utils.GetJsonFromRequest(r, &snapshotRequest)
	if err != nil {
		http.Error(w, "request unable to be parsed", http.StatusUnprocessableEntity)
		return
	}
	err = snapshotRequest.Validate()
	if err != nil {
		http.Error(w, "validation failed: "+err.Error(),
			http.StatusBadRequest)
		logger.LogError("validation failed: " + err.Error())
		return
	}

	var volume *VolumeEntry
	err = a.db.View(func(tx *bolt.Tx) error {
		var err error
		volume, err = NewVolumeEntryFromId(tx, id)
		if err == ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return err
		}
		return nil
	})
	if err != nil {
		return
	}
	snapshot := NewSnapshotEntryFromRequest(&snapshotRequest)
	snapshot.OriginVolumeID = id
	snapCreateOp := NewVolumeSnapshotOperation(volume, snapshot, a.db)
	if err := AsyncHttpOperation(a, w, r, snapCreateOp); err != nil {
		http.Error(w,
			fmt.Sprintf("Failed to take snapshot on volume: %v", err),
			http.StatusInternalServerError)
		return
	}
}

// returns func which stops (optionally) and deletes session from target volume to remote volume, then removes target volume
func asyncVolumeDelete(w http.ResponseWriter,
	r *http.Request,
	app *App,
	targetVolume *VolumeEntry,
	remoteVolume *VolumeEntry,
	targetNode *NodeEntry) error {
	return AsyncHttpRedirectFunc(app, w, r, func() (string, error) {
		logger.Info("Starting deleting volume %v", targetVolume.Info.Name)

		status, err := targetVolume.GeoReplicationStatus(app.executor, targetNode.ManageHostName())
		if err != nil {
			err = logger.LogError("Cat get geo-replication status %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return "", err
		}

		if status != "Stopped" {
			stopSessionReq := api.GeoReplicationRequest{
				Action: api.GeoReplicationActionStop,
				GeoReplicationInfo: api.GeoReplicationInfo{
					SlaveHost:   remoteVolume.Info.Mount.GlusterFS.Hosts[0],
					SlaveVolume: remoteVolume.Info.Name,
				},
			}
			if err := targetVolume.GeoReplicationAction(app.db, app.executor, targetNode.ManageHostName(), stopSessionReq); err != nil {
				err = logger.LogError("Error stopping session for volume %v: %v", targetVolume.Info.Name, err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return "", err
			}
		}

		deleteSessionReq := api.GeoReplicationRequest{
			Action: api.GeoReplicationActionDelete,
			GeoReplicationInfo: api.GeoReplicationInfo{
				SlaveHost:   remoteVolume.Info.Mount.GlusterFS.Hosts[0],
				SlaveVolume: remoteVolume.Info.Name,
			},
		}
		if err := targetVolume.GeoReplicationAction(app.db, app.executor, targetNode.ManageHostName(), deleteSessionReq); err != nil {
			err = logger.LogError("Error deleting session for volume %v: %v", targetVolume.Info.Name, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return "", err
		}

		deleteVolume := NewVolumeDeleteOperation(targetVolume, app.db)
		if err := AsyncHttpOperation(app, w, r, deleteVolume); err != nil {
			err = logger.LogError("Failed to delete volume %v: %v", targetVolume.Info.Name, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return "", err
		}

		return "/volumes", nil
	})
}
