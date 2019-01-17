// Copyright 2019 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package utils

import (
	"context"
	"io/ioutil"
	"time"

	"github.com/juju/errors"
	"google.golang.org/grpc"

	"github.com/pingcap/dm/dm/pb"
)

func CreateDmCtl(addr string) (pb.MasterClient, error) {
	conn, err := grpc.Dial(addr, grpc.WithInsecure(), grpc.WithBackoffMaxDelay(3*time.Second))
	if err != nil {
		return nil, errors.Trace(err)
	}
	return pb.NewMasterClient(conn), nil
}

func StartTask(ctx context.Context, cli pb.MasterClient, configFile string, workers []string) error {
	content, err := ioutil.ReadFile(configFile)
	if err != nil {
		return errors.Trace(err)
	}

	resp, err := cli.StartTask(ctx, &pb.StartTaskRequest{
		Task:    string(content),
		Workers: workers,
	})
	if err != nil {
		return errors.Trace(err)
	}

	for _, wp := range resp.GetWorkers() {
		if !wp.GetResult() {
			return errors.Errorf("fail to start task %v: %s", string(content), wp.GetMsg())
		}
	}

	return nil
}