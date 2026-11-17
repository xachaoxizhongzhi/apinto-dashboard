package contract

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eolinker/apinto-dashboard/common"
	"github.com/eolinker/apinto-dashboard/enum"
	dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	version "github.com/eolinker/apinto-dashboard/modules/base/version-entry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CompactJSON changes only inline Kitex sources. RawMessage preserves arbitrary
// fields and large JSON numbers in manually configured APIs and audit records.
func CompactJSON(raw []byte, put func(map[string]string) (string, error)) ([]byte, bool, error) {
	return compactJSON(raw, put, true)
}
func compactJSON(raw []byte, put func(map[string]string) (string, error), kitexConfig bool) ([]byte, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, false, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		changed := false
		if filesRaw, ok := object["thrift_files"]; kitexConfig && ok && object["main_idl"] != nil && object["service"] != nil && object["method"] != nil {
			var files map[string]string
			if err := json.Unmarshal(filesRaw, &files); err != nil {
				return nil, false, err
			}
			ref, err := put(files)
			if err != nil {
				return nil, false, err
			}
			if previous := object["contract"]; previous != nil {
				var old string
				if json.Unmarshal(previous, &old) != nil || old != ref {
					return nil, false, fmt.Errorf("内嵌契约与已有引用不一致")
				}
			}
			object["contract"], _ = json.Marshal(ref)
			delete(object, "thrift_files")
			changed = true
		}
		for key, value := range object {
			var pluginName string
			_ = json.Unmarshal(object["name"], &pluginName)
			next, modified, err := compactJSON(value, put, key == "config" && pluginName == "http_to_kitex")
			if err != nil {
				return nil, false, err
			}
			if modified {
				object[key] = next
				changed = true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(object)
		return out, true, err
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil && list != nil {
		changed := false
		for i, value := range list {
			next, modified, err := compactJSON(value, put, false)
			if err != nil {
				return nil, false, err
			}
			if modified {
				list[i] = next
				changed = true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(list)
		return out, true, err
	}
	// APIPlugin.Config is stored as a JSON string in native versions/history.
	var value string
	if kitexConfig && json.Unmarshal(raw, &value) == nil && strings.Contains(value, "thrift_files") && json.Valid([]byte(value)) {
		next, changed, err := compactJSON([]byte(value), put, true)
		if err != nil {
			return nil, false, err
		}
		if changed {
			out, err := json.Marshal(string(next))
			return out, true, err
		}
	}
	return raw, false, nil
}

// MigrateArtifacts is an explicit, restartable migration. Run with Dashboard
// stopped and a backup, before returning the native history columns to TEXT.
// Each row and all artifacts it references commit together; API IDs are unchanged.
func MigrateArtifacts(db *gorm.DB) error {
	if err := db.AutoMigrate(&entry.ContractArtifact{}, &entry.ContractVersion{}); err != nil {
		return err
	}
	return MigrateArtifactData(db, 0)
}

// MigrateArtifactData can run inside a transaction; schema changes are separate.
// namespace 0 migrates every workspace.
func MigrateArtifactData(db *gorm.DB, namespace int) error {
	for _, spec := range []struct{ table, column string }{{"version", "data"}, {"history", "old_value"}, {"history", "new_value"}, {"publish_history", "data"}} {
		if !db.Migrator().HasTable(spec.table) {
			continue
		}
		var ids []int
		query := db.Table(spec.table).Where("kind = ? AND "+spec.column+" LIKE ?", "api", "%thrift_files%")
		if namespace != 0 {
			query = query.Where("namespace = ?", namespace)
		}
		if err := query.Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			err := db.Transaction(func(tx *gorm.DB) error {
				var row struct {
					Namespace int
					Data      []byte
				}
				if err := tx.Table(spec.table).Select("namespace, "+spec.column+" AS data").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
					return err
				}
				raw, changed, err := CompactJSON(row.Data, func(files map[string]string) (string, error) { return PutArtifact(tx, row.Namespace, files) })
				if err != nil {
					return err
				}
				if !changed {
					return nil
				}
				// UpdateColumn preserves the original timestamp and version identity.
				values := map[string]interface{}{spec.column: raw}
				if spec.table == "version" {
					values["update_time"] = gorm.Expr("update_time")
				}
				return tx.Table(spec.table).Where("id = ?", id).UpdateColumns(values).Error
			})
			if err != nil {
				return fmt.Errorf("迁移 %s.%s #%d: %w", spec.table, spec.column, id, err)
			}
		}
	}
	var ids []int
	query := db.Model(&entry.ContractVersion{}).Where("artifact = ? OR artifact IS NULL", "")
	if namespace != 0 {
		query = query.Where("namespace_id = ?", namespace)
	}
	if err := query.Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := db.Transaction(func(tx *gorm.DB) error {
			var row entry.ContractVersion
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
				return err
			}
			var files map[string]string
			if err := json.Unmarshal(row.Files, &files); err != nil {
				return err
			}
			ref, err := PutArtifact(tx, row.NamespaceID, files)
			if err != nil {
				return err
			}
			raw, _, err := CompactJSON(row.APIs, func(files map[string]string) (string, error) { return PutArtifact(tx, row.NamespaceID, files) })
			if err != nil {
				return err
			}
			var snapshots []*dto.APIInfo
			if err = json.Unmarshal(raw, &snapshots); err != nil {
				return err
			}
			refs := make([]entry.ContractAPIRef, 0, len(snapshots))
			for _, input := range snapshots {
				// Preserve the exact snapshot, including disabled/removed methods and manual
				// policies. It must not be replaced with the current API's configuration.
				v := entry.APIVersionConfig{Scheme: input.Scheme, IsDisable: input.IsDisable, RequestPath: common.ReplaceRestfulPath(input.RequestPath, enum.RestfulLabel), RequestPathLabel: input.RequestPath, ServiceID: row.UpstreamID, ServiceName: input.ServiceName, Method: input.Method, ProxyPath: input.ProxyPath, Timeout: input.Timeout, Retry: input.Retry, Hosts: input.Hosts, Match: input.Match, Header: input.Header, Protocols: input.Protocols, TemplateUUID: input.TemplateUUID}
				for _, p := range input.Plugins {
					v.Plugins = append(v.Plugins, &entry.APIPlugin{Name: p.Name, Config: p.Config, Disable: p.Disable})
				}
				var api entry.API
				err := tx.Where("namespace = ? AND uuid = ?", row.NamespaceID, input.UUID).Take(&api).Error
				if err != nil && err != gorm.ErrRecordNotFound {
					return err
				}
				if input.TemplateUUID != "" {
					if err = tx.Table("plugin_template").Select("id").Where("namespace = ? AND uuid = ?", row.NamespaceID, input.TemplateUUID).Scan(&v.TemplateID).Error; err != nil {
						return err
					}
				}
				encoded, err := json.Marshal(v)
				if err != nil {
					return err
				}
				snapshot := version.Version{NamespaceID: row.NamespaceID, Target: api.Id, Kind: "api", Data: encoded, Operator: row.Operator, CreateTime: row.CreateTime}
				if err = tx.Create(&snapshot).Error; err != nil {
					return err
				}
				refs = append(refs, entry.ContractAPIRef{UUID: input.UUID, Name: input.ApiName, Desc: input.Desc, GroupUUID: input.GroupUUID, VersionID: snapshot.Id, VersionName: fmt.Sprintf("contract-%d-%d", row.Id, snapshot.Id)})
			}
			encoded, err := json.Marshal(refs)
			if err != nil {
				return err
			}
			return tx.Model(&row).Updates(map[string]interface{}{"artifact": ref, "files": []byte("{}"), "apis": encoded}).Error
		}); err != nil {
			return fmt.Errorf("迁移契约版本 #%d: %w", id, err)
		}
	}
	return nil
}
