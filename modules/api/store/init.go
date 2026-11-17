package api_store

import (
	"context"
	entry "github.com/eolinker/apinto-dashboard/modules/api/api-entry"
	"github.com/eolinker/apinto-dashboard/store"
	"github.com/eolinker/eosc/common/bean"
)

func InitStoreDB(db store.IDB) {
	if err := db.DB(context.Background()).AutoMigrate(&entry.ContractArtifact{}, &entry.ContractVersion{}, &entry.ContractBinding{}); err != nil {
		panic(err)
	}
	api := NewAPIStore(db)
	apiStat := NewAPIStatStore(db)
	apiVersion := newAPIVersionStore(db)
	apiHistory := newApiHistoryStore(db)
	apiPublishHistory := newApiPublishHistoryStore(db)

	bean.Injection(&api)
	bean.Injection(&apiStat)
	bean.Injection(&apiVersion)
	bean.Injection(&apiHistory)
	bean.Injection(&apiPublishHistory)
}
