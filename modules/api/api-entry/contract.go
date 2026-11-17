package api_entry

import "time"

// ContractVersion pins an artifact and the exact API versions in a full update.
type ContractVersion struct {
	IncludeUnannotated bool      `gorm:"not null;default:false" json:"include_unannotated"`
	Id                 int       `gorm:"primaryKey" json:"id"`
	NamespaceID        int       `gorm:"not null;index:idx_contract_version_upstream" json:"-"`
	UpstreamID         int       `gorm:"not null;index:idx_contract_version_upstream" json:"-"`
	Artifact           string    `gorm:"size:96;not null;default:''" json:"-"`
	Revision           string    `gorm:"size:64;not null" json:"revision"`
	Files              []byte    `gorm:"type:longblob;not null" json:"-"`
	APIs               []byte    `gorm:"type:longblob;not null" json:"-"`
	Operator           int       `json:"operator"`
	CreateTime         time.Time `gorm:"autoCreateTime" json:"create_time"`
}

func (*ContractVersion) TableName() string { return "api_contract_version" }

type ContractBinding struct {
	Source      string `gorm:"size:16;not null;default:auto"`
	Id          int    `gorm:"primaryKey"`
	NamespaceID int    `gorm:"not null;uniqueIndex:idx_contract_binding_identity"`
	UpstreamID  int    `gorm:"not null;uniqueIndex:idx_contract_binding_identity"`
	RPCService  string `gorm:"size:255;not null;uniqueIndex:idx_contract_binding_identity"`
	RPCMethod   string `gorm:"size:255;not null;uniqueIndex:idx_contract_binding_identity"`
	APIUUID     string `gorm:"size:255;not null;uniqueIndex:idx_contract_binding_api"`
}

func (*ContractBinding) TableName() string { return "api_contract_binding" }

// ContractArtifact is content addressed and immutable; source is stored once per namespace.
type ContractArtifact struct {
	NamespaceID int    `gorm:"primaryKey;autoIncrement:false"`
	Revision    string `gorm:"size:64;primaryKey"`
	Files       []byte `gorm:"type:longblob;not null"`
}

func (*ContractArtifact) TableName() string { return "api_contract_artifact" }

// ContractAPIRef stores only presentation metadata and a reference to the native API version.
type ContractAPIRef struct {
	Source      string `json:"source,omitempty"`
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Desc        string `json:"desc"`
	GroupUUID   string `json:"group_uuid"`
	VersionID   int    `json:"version_id"`
	VersionName string `json:"version_name"`
}
