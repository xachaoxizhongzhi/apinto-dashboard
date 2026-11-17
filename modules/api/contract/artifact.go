package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ArtifactRef(revision string) string { return "thrift-" + revision + "@transcode" }
func ArtifactRevision(ref string) (string, error) {
	revision := strings.TrimSuffix(strings.TrimPrefix(ref, "thrift-"), "@transcode")
	if len(revision) != 64 || ArtifactRef(revision) != ref {
		return "", fmt.Errorf("非法契约资源引用")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return "", fmt.Errorf("非法契约资源引用")
	}
	return revision, nil
}
func PutArtifact(db *gorm.DB, ns int, files map[string]string) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("契约文件为空")
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	revision := hex.EncodeToString(sum[:])
	artifact := entry.ContractArtifact{NamespaceID: ns, Revision: revision, Files: raw}
	if err = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&artifact).Error; err != nil {
		return "", err
	}
	// Verify a duplicate instead of overwriting an immutable artifact.
	var saved entry.ContractArtifact
	if err = db.Where("namespace_id = ? AND revision = ?", ns, revision).First(&saved).Error; err != nil {
		return "", err
	}
	if !bytes.Equal(saved.Files, raw) {
		return "", fmt.Errorf("契约内容校验失败")
	}
	return ArtifactRef(revision), nil
}
func LoadArtifact(db *gorm.DB, ns int, ref string) (map[string]string, error) {
	revision, err := ArtifactRevision(ref)
	if err != nil {
		return nil, err
	}
	var saved entry.ContractArtifact
	if err = db.Where("namespace_id = ? AND revision = ?", ns, revision).First(&saved).Error; err != nil {
		return nil, fmt.Errorf("读取契约资源失败: %w", err)
	}
	sum := sha256.Sum256(saved.Files)
	if hex.EncodeToString(sum[:]) != revision {
		return nil, fmt.Errorf("契约内容校验失败")
	}
	var files map[string]string
	if err = json.Unmarshal(saved.Files, &files); err != nil {
		return nil, err
	}
	return files, nil
}
