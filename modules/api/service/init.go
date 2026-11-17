package api_service

import (
	api_contract "github.com/eolinker/apinto-dashboard/modules/api"
	"github.com/eolinker/apinto-dashboard/modules/api/driver"
	"github.com/eolinker/eosc/common/bean"

	api_store "github.com/eolinker/apinto-dashboard/modules/api/store"

	"github.com/eolinker/apinto-dashboard/store"
)

func init() {

	api := NewAPIService()
	var contracts api_contract.IContractService = api.(*apiService)
	bean.Injection(&contracts)
	store.RegisterStore(api_store.InitStoreDB)
	apiDriverManager := newAPIDriverManager()
	apiHttp := driver.CreateAPIHttp("http")
	apiWebsocket := driver.CreateAPIWebsocket("http")
	apiDriverManager.RegisterDriver(DriverApiHTTP, apiHttp)
	apiDriverManager.RegisterDriver(DriverWebsocket, apiWebsocket)
	bean.Injection(&apiDriverManager)

	importCache := newImportCache()
	batchOnlineTaskCache := newBatchOnlineTaskCache()
	bean.Injection(&importCache)
	bean.Injection(&batchOnlineTaskCache)
	bean.Injection(&api)
}
