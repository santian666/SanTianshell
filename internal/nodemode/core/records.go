package core

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type NodeRecord struct {
	ID          int    `json:"id"`
	Country     string `json:"country"`
	Quantity    int    `json:"quantity"`
	CreatedDate string `json:"created_date"`
	RenewalDate string `json:"renewal_date"`
	Remark      string `json:"remark"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type DeletedRecord struct {
	ID          int            `json:"id"`
	VPSAlias    string         `json:"vps_alias"`
	VPSIP       string         `json:"vps_ip"`
	NodeGroup   string         `json:"node_group"`
	NodeTag     string         `json:"node_tag"`
	Filename    string         `json:"filename"`
	InboundPort int            `json:"inbound_port"`
	ConfigJSON  map[string]any `json:"config_json"`
	DeletedAt   string         `json:"deleted_at"`
}

const maxSQLiteInBatch = 500

func (s *Service) createDeletedRecord(alias string, filename string, config map[string]any) error {
	db, release, err := s.acquireDB()
	if err != nil {
		return err
	}
	defer release()
	vps, _ := s.getVPS(alias)
	data, _ := json.Marshal(config)
	_, err = db.Exec(
		`insert into deleted_records(vps_alias,vps_ip,node_group,node_tag,filename,inbound_port,config_json,deleted_at) values(?,?,?,?,?,?,?,?)`,
		alias,
		vps.Address,
		extractGroupName(config),
		extractNodeTag(config),
		filename,
		extractListenPort(config),
		protectDBSecret(string(data)),
		time.Now().Format("2006-01-02 15:04:05"),
	)
	return err
}

func (s *Service) createDeletedRecords(alias string, items map[string]map[string]any) error {
	if len(items) == 0 {
		return nil
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return err
	}
	defer release()
	vps, _ := s.getVPS(alias)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`insert into deleted_records(vps_alias,vps_ip,node_group,node_tag,filename,inbound_port,config_json,deleted_at) values(?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	now := time.Now().Format("2006-01-02 15:04:05")
	for filename, config := range items {
		data, _ := json.Marshal(config)
		if _, err := stmt.Exec(alias, vps.Address, extractGroupName(config), extractNodeTag(config), filename, extractListenPort(config), protectDBSecret(string(data)), now); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) GetNodeRecords() ([]NodeRecord, error) {
	db, release, err := s.acquireDB()
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := db.Query(`select id,country,quantity,coalesce(created_date,''),coalesce(renewal_date,''),coalesce(remark,''),coalesce(created_at,''),coalesce(updated_at,'') from node_records order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []NodeRecord{}
	for rows.Next() {
		var item NodeRecord
		if err := rows.Scan(&item.ID, &item.Country, &item.Quantity, &item.CreatedDate, &item.RenewalDate, &item.Remark, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) SaveNodeRecord(record NodeRecord) error {
	db, release, err := s.acquireDB()
	if err != nil {
		return err
	}
	defer release()
	now := time.Now().Format("2006-01-02 15:04:05")
	if record.ID > 0 {
		_, err = db.Exec(`update node_records set country=?,quantity=?,created_date=?,renewal_date=?,remark=?,updated_at=? where id=?`, record.Country, record.Quantity, record.CreatedDate, record.RenewalDate, record.Remark, now, record.ID)
		return err
	}
	_, err = db.Exec(`insert into node_records(country,quantity,created_date,renewal_date,remark,created_at,updated_at) values(?,?,?,?,?,?,?)`, record.Country, record.Quantity, record.CreatedDate, record.RenewalDate, record.Remark, now, now)
	return err
}

func (s *Service) DeleteNodeRecord(id int) error {
	db, release, err := s.acquireDB()
	if err != nil {
		return err
	}
	defer release()
	_, err = db.Exec(`delete from node_records where id=?`, id)
	return err
}

func (s *Service) GetDeletedRecords() ([]DeletedRecord, error) {
	db, release, err := s.acquireDB()
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := db.Query(`select id,coalesce(vps_alias,''),coalesce(vps_ip,''),coalesce(node_group,''),coalesce(node_tag,''),coalesce(filename,''),coalesce(inbound_port,0),coalesce(config_json,'{}'),coalesce(deleted_at,'') from deleted_records order by id desc limit 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DeletedRecord{}
	for rows.Next() {
		var item DeletedRecord
		var configText string
		if err := rows.Scan(&item.ID, &item.VPSAlias, &item.VPSIP, &item.NodeGroup, &item.NodeTag, &item.Filename, &item.InboundPort, &configText, &item.DeletedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(unprotectDBSecret(configText)), &item.ConfigJSON)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) RestoreDeletedRecord(id int) (OperationResult, error) {
	return s.restoreDeletedRecordsBatch([]int{id})
}

func (s *Service) RollbackDeletedNodes(alias string, filenames []string) (OperationResult, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" || len(filenames) == 0 {
		return OperationResult{Success: true, Message: "没有需要恢复的节点"}, nil
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, filename := range filenames {
		filename = strings.TrimSpace(filename)
		if filename == "" || seen[filename] {
			continue
		}
		seen[filename] = true
		clean = append(clean, filename)
	}
	if len(clean) == 0 {
		return OperationResult{Success: true, Message: "没有需要恢复的节点"}, nil
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return OperationResult{}, err
	}
	defer release()
	type rollbackItem struct {
		id       int
		filename string
		config   map[string]any
	}
	items := []rollbackItem{}
	restoredFile := map[string]bool{}
	for start := 0; start < len(clean); start += maxSQLiteInBatch {
		end := start + maxSQLiteInBatch
		if end > len(clean) {
			end = len(clean)
		}
		chunk := clean[start:end]
		placeholders := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		args = append(args, alias)
		for _, filename := range chunk {
			placeholders = append(placeholders, "?")
			args = append(args, filename)
		}
		rows, err := db.Query(`select id,coalesce(filename,''),coalesce(config_json,'{}') from deleted_records where vps_alias=? and filename in (`+strings.Join(placeholders, ",")+`) order by id desc`, args...)
		if err != nil {
			return OperationResult{}, err
		}
		for rows.Next() {
			var item rollbackItem
			var configText string
			if err := rows.Scan(&item.id, &item.filename, &configText); err != nil {
				_ = rows.Close()
				return OperationResult{}, err
			}
			if item.filename == "" || restoredFile[item.filename] {
				continue
			}
			if err := json.Unmarshal([]byte(unprotectDBSecret(configText)), &item.config); err != nil {
				_ = rows.Close()
				return OperationResult{}, err
			}
			restoredFile[item.filename] = true
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return OperationResult{}, err
		}
		if err := rows.Close(); err != nil {
			return OperationResult{}, err
		}
	}
	if len(items) == 0 {
		return OperationResult{Success: false, Message: "没有找到可恢复的删除记录"}, nil
	}
	unlock := s.lockNodeConfigAliases(alias)
	defer unlock()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return OperationResult{}, err
	}
	for _, item := range items {
		if _, exists := configs[item.filename]; exists {
			return OperationResult{Success: false, Message: "同名节点已存在，无法恢复：" + alias + " / " + item.filename}, nil
		}
	}
	for _, item := range items {
		configs[item.filename] = item.config
	}
	if err := s.saveNodeConfigMap(alias, configs); err != nil {
		return OperationResult{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return OperationResult{}, err
	}
	stmt, err := tx.Prepare(`delete from deleted_records where id=?`)
	if err != nil {
		_ = tx.Rollback()
		return OperationResult{}, err
	}
	restored := []string{}
	for _, item := range items {
		if _, err := stmt.Exec(item.id); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return OperationResult{}, err
		}
		restored = append(restored, item.filename)
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: "已恢复本地节点", Count: len(restored), Items: restored}, nil
}

func (s *Service) restoreDeletedRecordsBatch(ids []int) (OperationResult, error) {
	if len(ids) == 0 {
		return OperationResult{Success: true, Message: "没有需要恢复的记录"}, nil
	}
	db, release, err := s.acquireDB()
	if err != nil {
		return OperationResult{}, err
	}
	defer release()
	type restoreItem struct {
		id       int
		alias    string
		filename string
		config   map[string]any
	}
	items := []restoreItem{}
	seenIDs := map[int]bool{}
	for start := 0; start < len(ids); start += maxSQLiteInBatch {
		end := start + maxSQLiteInBatch
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		rows, err := db.Query(`select id,coalesce(vps_alias,''),coalesce(filename,''),coalesce(config_json,'{}') from deleted_records where id in (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return OperationResult{}, err
		}
		for rows.Next() {
			var item restoreItem
			var configText string
			if err := rows.Scan(&item.id, &item.alias, &item.filename, &configText); err != nil {
				_ = rows.Close()
				return OperationResult{}, err
			}
			if item.alias == "" || item.filename == "" || seenIDs[item.id] {
				continue
			}
			if err := json.Unmarshal([]byte(unprotectDBSecret(configText)), &item.config); err != nil {
				_ = rows.Close()
				return OperationResult{}, err
			}
			seenIDs[item.id] = true
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return OperationResult{}, err
		}
		if err := rows.Close(); err != nil {
			return OperationResult{}, err
		}
	}
	if len(items) == 0 {
		return OperationResult{Success: false, Message: "删除记录不存在"}, nil
	}
	byAlias := map[string][]restoreItem{}
	for _, item := range items {
		byAlias[item.alias] = append(byAlias[item.alias], item)
	}
	aliases := make([]string, 0, len(byAlias))
	for alias := range byAlias {
		aliases = append(aliases, alias)
	}
	unlock := s.lockNodeConfigAliases(aliases...)
	defer unlock()
	configsByAlias := map[string]map[string]map[string]any{}
	for alias, group := range byAlias {
		configs, err := s.loadNodeConfigMap(alias)
		if err != nil {
			return OperationResult{}, err
		}
		seen := map[string]bool{}
		for _, item := range group {
			if _, exists := configs[item.filename]; exists {
				return OperationResult{Success: false, Message: "同名节点已存在，无法恢复：" + alias + " / " + item.filename}, nil
			}
			if seen[item.filename] {
				return OperationResult{Success: false, Message: "存在重复删除记录，无法恢复：" + alias + " / " + item.filename}, nil
			}
			seen[item.filename] = true
		}
		configsByAlias[alias] = configs
	}
	for alias, group := range byAlias {
		configs := configsByAlias[alias]
		for _, item := range group {
			configs[item.filename] = item.config
		}
		if err := s.saveNodeConfigMap(alias, configs); err != nil {
			return OperationResult{}, err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return OperationResult{}, err
	}
	stmt, err := tx.Prepare(`delete from deleted_records where id=?`)
	if err != nil {
		_ = tx.Rollback()
		return OperationResult{}, err
	}
	restored := []string{}
	for _, item := range items {
		if _, err := stmt.Exec(item.id); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return OperationResult{}, err
		}
		restored = append(restored, item.alias+"::"+item.filename)
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Success: true, Message: "恢复完成", Count: len(restored), Items: restored}, nil
}
func (s *Service) openDB() (*sql.DB, error) {
	path := filepath.Join(s.dataDir, "sb_admin.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Service) acquireDB() (*sql.DB, func(), error) {
	s.dbMu.Lock()
	persistent := s.persistentDB
	if persistent {
		if s.sharedDB == nil {
			db, err := s.openDB()
			if err != nil {
				s.dbMu.Unlock()
				return nil, nil, err
			}
			s.sharedDB = db
		}
		if !s.schemaReady {
			if err := ensureFeatureTables(s.sharedDB); err != nil {
				s.dbMu.Unlock()
				return nil, nil, err
			}
			s.schemaReady = true
		}
		db := s.sharedDB
		s.dbMu.Unlock()
		return db, func() {}, nil
	}
	needSchema := !s.schemaReady
	s.dbMu.Unlock()

	db, err := s.openDB()
	if err != nil {
		return nil, nil, err
	}
	if needSchema {
		if err := ensureFeatureTables(db); err != nil {
			_ = db.Close()
			return nil, nil, err
		}
		s.dbMu.Lock()
		s.schemaReady = true
		s.dbMu.Unlock()
	}
	return db, func() { _ = db.Close() }, nil
}

func ensureRecordTables(db *sql.DB) error {
	schema := strings.Join([]string{
		`create table if not exists node_records (id integer primary key autoincrement,country text not null,quantity integer not null default 1,created_date date,renewal_date date,remark text,created_at timestamp default current_timestamp,updated_at timestamp default current_timestamp);`,
		`create table if not exists deleted_records (id integer primary key autoincrement,vps_alias text,vps_ip text,node_group text,node_tag text,filename text,inbound_port integer,config_json text,deleted_at timestamp default current_timestamp);`,
		`create index if not exists idx_node_records_country on node_records(country);`,
		`create index if not exists idx_deleted_records_vps_alias on deleted_records(vps_alias);`,
	}, "\n")
	_, err := db.Exec(schema)
	return err
}
