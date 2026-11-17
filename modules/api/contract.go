package api

import (
	"context"
	api_dto "github.com/eolinker/apinto-dashboard/modules/api/api-dto"
	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	model "github.com/eolinker/apinto-dashboard/modules/api/model"
)

type ContractInfo struct {
	Sources            map[string]string        `json:"-"`
	IncludeUnannotated bool                     `json:"include_unannotated"`
	VersionID          int                      `json:"version_id"`
	Revision           string                   `json:"revision"`
	Files              map[string]string        `json:"files"`
	Versions           []*entry.ContractVersion `json:"versions"`
	APIs               []*api_dto.APIInfo       `json:"apis"`
}
type ContractManualAPI struct {
	BaseVersion int    `json:"base_version"`
	Service     string `json:"service"`
	Method      string `json:"method"`
	File        string `json:"file"`
	HTTPMethod  string `json:"http_method"`
	Path        string `json:"path"`
}
type IContractService interface {
	CreateContractAPI(context.Context, int, int, string, *ContractManualAPI) (*ContractInfo, error)
	GetContract(context.Context, int, string, int) (*ContractInfo, error)
	SaveContract(context.Context, int, int, string, int, *contract.Bundle) (*ContractInfo, error)
	RestoreContract(context.Context, int, int, string, int, int) (*ContractInfo, error)
	PublishContract(context.Context, int, int, string, int, []string) ([]*model.BatchListItem, error)
}
