package filter

import (
	"fmt"
	"regexp"
	"strings"
)

// Table represents a table.
type Table struct {
	Schema string `toml:"db-name" json:"db-name" yaml:"db-name"`
	Name   string `toml:"tbl-name" json:"tbl-name" yaml:"tbl-name"`
}

// String implements the fmt.Stringer interface.
func (t *Table) String() string {
	return fmt.Sprintf("`%s`.`%s`", t.Schema, t.Name)
}

// Rules contains Filter rules.
type Rules struct {
	DoTables []*Table `json:"do-tables" yaml:"do-tables"`
	DoDBs    []string `json:"do-dbs" yaml:"do-dbs"`

	IgnoreTables []*Table `json:"ignore-tables" yaml:"ignore-tables"`
	IgnoreDBs    []string `json:"ignore-dbs" yaml:"ignore-dbs"`
}

// ToLower convert all entries to lowercase
func (r *Rules) ToLower() {
	if r == nil {
		return
	}

	for _, table := range r.DoTables {
		table.Name = strings.ToLower(table.Name)
		table.Schema = strings.ToLower(table.Schema)
	}
	for _, table := range r.IgnoreTables {
		table.Name = strings.ToLower(table.Name)
		table.Schema = strings.ToLower(table.Schema)
	}
	for i, db := range r.IgnoreDBs {
		r.IgnoreDBs[i] = strings.ToLower(db)
	}
	for i, db := range r.DoDBs {
		r.DoDBs[i] = strings.ToLower(db)
	}
}

// Filter implements whitelist and blacklist filters.
type Filter struct {
	patternMap map[string]*regexp.Regexp
	rules      *Rules
}

// New creates a filter use the rules.
func New(rules *Rules) *Filter {
	f := &Filter{}
	f.rules = rules
	f.patternMap = make(map[string]*regexp.Regexp)
	f.genRegexMap()
	return f
}

func (f *Filter) genRegexMap() {
	if f.rules == nil {
		return
	}

	for _, db := range f.rules.DoDBs {
		f.addOneRegex(db)
	}

	for _, table := range f.rules.DoTables {
		f.addOneRegex(table.Schema)
		f.addOneRegex(table.Name)
	}

	for _, db := range f.rules.IgnoreDBs {
		f.addOneRegex(db)
	}

	for _, table := range f.rules.IgnoreTables {
		f.addOneRegex(table.Schema)
		f.addOneRegex(table.Name)
	}
}

func (f *Filter) addOneRegex(originStr string) {
	if _, ok := f.patternMap[originStr]; !ok {
		var re *regexp.Regexp
		if originStr[0] != '~' {
			re = regexp.MustCompile(fmt.Sprintf("(?i)^%s$", originStr))
		} else {
			re = regexp.MustCompile(fmt.Sprintf("(?i)%s", originStr[1:]))
		}
		f.patternMap[originStr] = re
	}
}

// ApplyOn applies filter rules on tables
// rules like
// https://dev.mysql.com/doc/refman/8.0/en/replication-rules-table-options.html
// https://dev.mysql.com/doc/refman/8.0/en/replication-rules-db-options.html
func (f *Filter) ApplyOn(stbs []*Table) []*Table {
	if f == nil || f.rules == nil {
		return stbs
	}

	var tbs []*Table
	for _, tb := range stbs {
		if f.filterOnSchemas(tb) && f.filterOnTables(tb) {
			tbs = append(tbs, tb)
		}
	}

	return tbs
}

func (f *Filter) filterOnSchemas(tb *Table) bool {
	if len(f.rules.DoDBs) > 0 {
		// not macthed do db rules, ignore update
		if !f.findMatchedDoDBs(tb) {
			return false
		}
	} else if len(f.rules.IgnoreDBs) > 0 {
		//  macthed ignore db rules, ignore update
		if f.findMatchedIgnoreDBs(tb) {
			return false
		}
	}

	return true
}

func (f *Filter) findMatchedDoDBs(tb *Table) bool {
	return f.matchDB(f.rules.DoDBs, tb.Schema)
}

func (f *Filter) findMatchedIgnoreDBs(tb *Table) bool {
	return f.matchDB(f.rules.IgnoreDBs, tb.Schema)
}

func (f *Filter) filterOnTables(tb *Table) bool {
	// schema statement like create/drop/alter database
	if len(tb.Name) == 0 {
		return true
	}

	if len(f.rules.DoTables) > 0 {
		if f.findMatchedDoTables(tb) {
			return true
		}
	}

	if len(f.rules.IgnoreTables) > 0 {
		if f.findMatchedIgnoreTables(tb) {
			return false
		}
	}

	return len(f.rules.DoTables) == 0
}

func (f *Filter) findMatchedDoTables(tb *Table) bool {
	return f.matchTable(f.rules.DoTables, tb)
}

func (f *Filter) findMatchedIgnoreTables(tb *Table) bool {
	return f.matchTable(f.rules.IgnoreTables, tb)
}

func (f *Filter) matchDB(patternDBS []string, a string) bool {
	for _, b := range patternDBS {
		if f.matchString(b, a) {
			return true
		}
	}
	return false
}

func (f *Filter) matchTable(patternTBS []*Table, tb *Table) bool {
	for _, ptb := range patternTBS {
		if f.matchString(ptb.Schema, tb.Schema) && f.matchString(ptb.Name, tb.Name) {
			return true
		}
	}

	return false
}

func (f *Filter) matchString(pattern string, t string) bool {
	if re, ok := f.patternMap[pattern]; ok {
		return re.MatchString(t)
	}
	return pattern == t
}