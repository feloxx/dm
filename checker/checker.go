package checker

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql" // for mysql
	"github.com/juju/errors"
	"github.com/ngaut/log"
	"github.com/pingcap/tidb-enterprise-tools/dm/config"
	"github.com/pingcap/tidb-enterprise-tools/dm/pb"
	"github.com/pingcap/tidb-enterprise-tools/dm/unit"
	"github.com/pingcap/tidb-enterprise-tools/pkg/filter"
	"github.com/pingcap/tidb-enterprise-tools/pkg/utils"
	"github.com/pingcap/tidb-tools/pkg/check"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb-tools/pkg/table-router"
	"github.com/siddontang/go/sync2"
	"golang.org/x/net/context"
)

type mysqlInstance struct {
	cfg *config.SubTaskConfig

	sourceDB     *sql.DB
	sourceDBinfo *dbutil.DBConfig

	targetDB     *sql.DB
	targetDBInfo *dbutil.DBConfig
}

// Checker performs pre-check of data synchronization
type Checker struct {
	closed sync2.AtomicBool

	instances []*mysqlInstance

	checkList []check.Checker
	result    struct {
		sync.RWMutex
		detail *check.Results
	}
}

// NewChecker returns a checker
func NewChecker(cfgs []*config.SubTaskConfig) *Checker {
	c := &Checker{
		instances: make([]*mysqlInstance, 0, len(cfgs)),
	}

	for _, cfg := range cfgs {
		c.instances = append(c.instances, &mysqlInstance{
			cfg: cfg,
		})
	}

	return c
}

// Init implements Unit interface
func (c *Checker) Init() error {

	// target name => instance => schema => [tables]
	sharding := make(map[string]map[string]map[string][]string)
	shardingCounter := make(map[string]int)
	dbs := make(map[string]*sql.DB)
	for _, instance := range c.instances {
		instanceID := fmt.Sprintf("%s:%d", instance.cfg.From.Host, instance.cfg.From.Port)
		bw := filter.New(instance.cfg.BWList)
		r, err := router.NewTableRouter(instance.cfg.RouteRules)
		if err != nil {
			return errors.Trace(err)
		}

		instance.sourceDBinfo = &dbutil.DBConfig{
			Host:     instance.cfg.From.Host,
			Port:     instance.cfg.From.Port,
			User:     instance.cfg.From.User,
			Password: instance.cfg.From.Password,
		}
		instance.sourceDB, err = dbutil.OpenDB(*instance.sourceDBinfo)
		if err != nil {
			return errors.Trace(err)
		}

		instance.targetDBInfo = &dbutil.DBConfig{
			Host:     instance.cfg.To.Host,
			Port:     instance.cfg.To.Port,
			User:     instance.cfg.To.User,
			Password: instance.cfg.To.Password,
		}
		instance.targetDB, err = dbutil.OpenDB(*instance.targetDBInfo)
		if err != nil {
			return errors.Trace(err)
		}

		mapping, err := utils.FetchTargetDoTables(instance.sourceDB, bw, r)
		if err != nil {
			return errors.Trace(err)
		}

		checkTables := make(map[string][]string)
		for name, tables := range mapping {
			for _, table := range tables {
				checkTables[table.Schema] = append(checkTables[table.Schema], table.Name)
				if _, ok := sharding[name]; !ok {
					sharding[name] = make(map[string]map[string][]string)
				}
				if _, ok := sharding[name][instanceID]; !ok {
					sharding[name][instanceID] = make(map[string][]string)
				}
				if _, ok := sharding[name][instanceID][table.Schema]; !ok {
					sharding[name][instanceID][table.Schema] = make([]string, 0, 1)
				}

				sharding[name][instanceID][table.Schema] = append(sharding[name][instanceID][table.Schema], table.Name)
				shardingCounter[name]++
			}
		}
		dbs[instanceID] = instance.sourceDB

		c.checkList = append(c.checkList, check.NewMySQLBinlogEnableChecker(instance.sourceDB, instance.sourceDBinfo))
		c.checkList = append(c.checkList, check.NewMySQLBinlogFormatChecker(instance.sourceDB, instance.sourceDBinfo))
		c.checkList = append(c.checkList, check.NewMySQLBinlogRowImageChecker(instance.sourceDB, instance.sourceDBinfo))
		c.checkList = append(c.checkList, check.NewSourcePrivilegeChecker(instance.sourceDB, instance.sourceDBinfo))
		c.checkList = append(c.checkList, check.NewTablesChecker(instance.sourceDB, instance.sourceDBinfo, checkTables))
	}

	for name, shardingSet := range sharding {
		if shardingCounter[name] <= 1 {
			continue
		}

		c.checkList = append(c.checkList, check.NewShardingTablesCheck(name, dbs, shardingSet))
	}

	return nil
}

// Process implements Unit interface
func (c *Checker) Process(ctx context.Context, pr chan pb.ProcessResult) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	isCanceled := false
	errs := make([]*pb.ProcessError, 0, 1)
	result, _ := check.Do(cctx, c.checkList)
	if !result.Summary.Passed {
		errs = append(errs, unit.NewProcessError(pb.ErrorType_CheckFailed, "check was failed, please see detail"))

	}

	select {
	case <-cctx.Done():
		isCanceled = true
	default:
	}

	rawResult, err := json.Marshal(result)
	if err != nil {
		rawResult = []byte(fmt.Sprintf("marshal error %v", err))
	}

	c.result.Lock()
	c.result.detail = result
	c.result.Unlock()

	pr <- pb.ProcessResult{
		IsCanceled: isCanceled,
		Errors:     errs,
		Detail:     rawResult,
	}
}

// Close implements Unit interface
func (c *Checker) Close() {
	if c.closed.Get() {
		return
	}

	for _, instance := range c.instances {
		if instance.sourceDB != nil {
			if err := dbutil.CloseDB(instance.sourceDB); err != nil {
				log.Errorf("close source db %+v error %v", instance.sourceDBinfo, err)
			}
		}

		if instance.targetDB != nil {
			if err := dbutil.CloseDB(instance.targetDB); err != nil {
				log.Errorf("close target db %+v error %v", instance.targetDBInfo, err)
			}
		}
	}

	c.closed.Set(true)
}

// Pause implements Unit interface
func (c *Checker) Pause() {
	if c.closed.Get() {
		log.Warn("[checker] try to pause, but already closed")
		return
	}
}

// Resume resumes the paused process
func (c *Checker) Resume(ctx context.Context, pr chan pb.ProcessResult) {
	if c.closed.Get() {
		log.Warn("[checker] try to resume, but already closed")
		return
	}

	c.Process(ctx, pr)
}

// Type implements Unit interface
func (c *Checker) Type() pb.UnitType {
	return pb.UnitType_Check
}

// Status implements Unit interface
func (c *Checker) Status() interface{} {
	c.result.RLock()
	res := c.result.detail
	c.result.RUnlock()

	var errs []*pb.ProcessError
	if !res.Summary.Passed {
		errs = append(errs, unit.NewProcessError(pb.ErrorType_CheckFailed, "check was failed, please see detail"))
	}
	rawResult, err := json.Marshal(res)
	if err != nil {
		errs = append(errs, unit.NewProcessError(pb.ErrorType_UnknownError, fmt.Sprintf("marshal error %v", err)))
	}

	return &pb.CheckStatus{
		Passed:     res.Summary.Passed,
		Total:      int32(res.Summary.Total),
		Failed:     int32(res.Summary.Failed),
		Successful: int32(res.Summary.Successful),
		Warning:    int32(res.Summary.Warning),
		Detail:     rawResult,
	}
}