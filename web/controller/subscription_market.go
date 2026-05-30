package controller

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

type SubscriptionMarketAPIController struct {
	BaseController
	subscriptionMarket service.SubscriptionMarketService
}

type CustomerSubscriptionPublicController struct {
	subscriptionMarket service.SubscriptionMarketService
}

func NewSubscriptionMarketAPIController(g *gin.RouterGroup) *SubscriptionMarketAPIController {
	a := &SubscriptionMarketAPIController{}
	a.initRouter(g)
	return a
}

func NewCustomerSubscriptionPublicController(g *gin.RouterGroup) *CustomerSubscriptionPublicController {
	a := &CustomerSubscriptionPublicController{}
	a.initRouter(g)
	return a
}

func (a *SubscriptionMarketAPIController) initRouter(g *gin.RouterGroup) {
	upstreams := g.Group("/upstreams")
	upstreams.GET("/list", a.listUpstreams)
	upstreams.POST("/add", a.addUpstream)
	upstreams.POST("/update/:id", a.updateUpstream)
	upstreams.POST("/delete/:id", a.deleteUpstream)
	upstreams.POST("/sync/:id", a.syncUpstream)
	upstreams.POST("/toggle/:id", a.toggleUpstream)

	nodes := g.Group("/nodes")
	nodes.GET("/list", a.listNodes)
	nodes.POST("/toggle/:id", a.toggleNode)
	nodes.POST("/bulk-toggle", a.bulkToggleNodes)

	customers := g.Group("/customers")
	customers.GET("/list", a.listCustomers)
	customers.POST("/add", a.addCustomer)
	customers.POST("/update/:id", a.updateCustomer)
	customers.POST("/toggle/:id", a.toggleCustomer)
	customers.POST("/delete/:id", a.deleteCustomer)

	inbounds := g.Group("/inbounds")
	inbounds.GET("/:id/nodes", a.getInboundNodes)
	inbounds.POST("/:id/nodes", a.updateInboundNodes)
}

func (a *CustomerSubscriptionPublicController) initRouter(g *gin.RouterGroup) {
	g.GET("/customer-sub/:token", a.customerSubscription)
}

type upstreamSubscriptionForm struct {
	Name   string `json:"name" form:"name"`
	Url    string `json:"url" form:"url"`
	Enable bool   `json:"enable" form:"enable"`
}

type toggleForm struct {
	Enable bool `json:"enable" form:"enable"`
}

type nodeBulkToggleForm struct {
	NodeIds []int `json:"nodeIds" form:"nodeIds"`
	Enable  bool  `json:"enable" form:"enable"`
}

type customerSubscriptionForm struct {
	Name       string `json:"name" form:"name"`
	Enable     bool   `json:"enable" form:"enable"`
	ExpiryTime int64  `json:"expiryTime" form:"expiryTime"`
	NodeIds    []int  `json:"nodeIds" form:"nodeIds"`
}

type nodeSelectionForm struct {
	NodeIds []int `json:"nodeIds" form:"nodeIds"`
}

func (a *SubscriptionMarketAPIController) listUpstreams(c *gin.Context) {
	upstreams, err := a.subscriptionMarket.GetUpstreams()
	jsonObj(c, upstreams, err)
}

func (a *SubscriptionMarketAPIController) addUpstream(c *gin.Context) {
	var form upstreamSubscriptionForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "add upstream subscription", err)
		return
	}
	upstream, err := a.subscriptionMarket.CreateUpstream(form.Name, form.Url, form.Enable)
	if err != nil {
		jsonMsg(c, "add upstream subscription", err)
		return
	}
	if synced, syncErr := a.subscriptionMarket.SyncUpstream(upstream.Id); synced != nil {
		upstream = synced
	} else if syncErr != nil {
		upstream.LastError = syncErr.Error()
	}
	jsonMsgObj(c, "add upstream subscription", upstream, nil)
}

func (a *SubscriptionMarketAPIController) updateUpstream(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	var form upstreamSubscriptionForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "update upstream subscription", err)
		return
	}
	upstream, err := a.subscriptionMarket.UpdateUpstream(id, form.Name, form.Url, form.Enable)
	if err != nil {
		jsonMsg(c, "update upstream subscription", err)
		return
	}
	if synced, syncErr := a.subscriptionMarket.SyncUpstream(upstream.Id); synced != nil {
		upstream = synced
	} else if syncErr != nil {
		upstream.LastError = syncErr.Error()
	}
	jsonMsgObj(c, "update upstream subscription", upstream, nil)
}

func (a *SubscriptionMarketAPIController) deleteUpstream(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	err := a.subscriptionMarket.DeleteUpstream(id)
	jsonMsg(c, "delete upstream subscription", err)
}

func (a *SubscriptionMarketAPIController) syncUpstream(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	upstream, err := a.subscriptionMarket.SyncUpstream(id)
	jsonMsgObj(c, "sync upstream subscription", upstream, err)
}

func (a *SubscriptionMarketAPIController) toggleUpstream(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	var form toggleForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "toggle upstream subscription", err)
		return
	}
	err := a.subscriptionMarket.SetUpstreamEnable(id, form.Enable)
	jsonMsg(c, "toggle upstream subscription", err)
}

func (a *SubscriptionMarketAPIController) listNodes(c *gin.Context) {
	enabledOnly := c.Query("enabledOnly") == "1" || strings.EqualFold(c.Query("enabledOnly"), "true")
	nodes, err := a.subscriptionMarket.GetNodes(enabledOnly)
	jsonObj(c, nodes, err)
}

func (a *SubscriptionMarketAPIController) toggleNode(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	var form toggleForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "toggle upstream node", err)
		return
	}
	err := a.subscriptionMarket.SetNodeEnable(id, form.Enable)
	jsonMsg(c, "toggle upstream node", err)
}

func (a *SubscriptionMarketAPIController) bulkToggleNodes(c *gin.Context) {
	var form nodeBulkToggleForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "bulk toggle upstream nodes", err)
		return
	}
	err := a.subscriptionMarket.SetNodesEnable(form.NodeIds, form.Enable)
	jsonMsg(c, "bulk toggle upstream nodes", err)
}

func (a *SubscriptionMarketAPIController) listCustomers(c *gin.Context) {
	customers, err := a.subscriptionMarket.GetCustomers()
	if err == nil {
		for i := range customers {
			customers[i].SubscriptionURL = buildCustomerSubscriptionURL(c, customers[i].Token)
		}
	}
	jsonObj(c, customers, err)
}

func (a *SubscriptionMarketAPIController) addCustomer(c *gin.Context) {
	var form customerSubscriptionForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "add customer subscription", err)
		return
	}
	customer, err := a.subscriptionMarket.CreateCustomer(form.Name, form.Enable, form.ExpiryTime, form.NodeIds)
	if err == nil && customer != nil {
		customer.SubscriptionURL = buildCustomerSubscriptionURL(c, customer.Token)
	}
	jsonMsgObj(c, "add customer subscription", customer, err)
}

func (a *SubscriptionMarketAPIController) updateCustomer(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	var form customerSubscriptionForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "update customer subscription", err)
		return
	}
	customer, err := a.subscriptionMarket.UpdateCustomer(id, form.Name, form.Enable, form.ExpiryTime, form.NodeIds)
	if err == nil && customer != nil {
		customer.SubscriptionURL = buildCustomerSubscriptionURL(c, customer.Token)
	}
	jsonMsgObj(c, "update customer subscription", customer, err)
}

func (a *SubscriptionMarketAPIController) toggleCustomer(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	var form toggleForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "toggle customer subscription", err)
		return
	}
	err := a.subscriptionMarket.SetCustomerEnable(id, form.Enable)
	jsonMsg(c, "toggle customer subscription", err)
}

func (a *SubscriptionMarketAPIController) deleteCustomer(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	err := a.subscriptionMarket.DeleteCustomer(id)
	jsonMsg(c, "delete customer subscription", err)
}

func (a *SubscriptionMarketAPIController) getInboundNodes(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	nodeIDs, err := a.subscriptionMarket.GetInboundNodeIDs(id)
	jsonObj(c, nodeIDs, err)
}

func (a *SubscriptionMarketAPIController) updateInboundNodes(c *gin.Context) {
	id, ok := parsePositiveID(c, c.Param("id"))
	if !ok {
		return
	}
	var form nodeSelectionForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, "update inbound upstream nodes", err)
		return
	}
	err := a.subscriptionMarket.SetInboundNodes(id, form.NodeIds)
	jsonMsg(c, "update inbound upstream nodes", err)
}

func (a *CustomerSubscriptionPublicController) customerSubscription(c *gin.Context) {
	token := c.Param("token")
	content, err := a.subscriptionMarket.GetCustomerSubscription(token)
	if err != nil {
		writeCustomerSubscriptionError(c, err)
		return
	}

	setCustomerSubscriptionNoCacheHeaders(c)
	expire := int64(0)
	if content.Customer.ExpiryTime > 0 {
		expire = content.Customer.ExpiryTime / 1000
	}
	c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=0; total=0; expire=%d", expire))
	c.Header("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(content.Customer.Name)))

	if strings.EqualFold(c.Query("format"), "clash") {
		clash, err := a.subscriptionMarket.BuildClashSubscription(content)
		if err != nil {
			writeCustomerSubscriptionError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", []byte(clash))
		return
	}

	if len(content.Links) == 0 {
		writeCustomerSubscriptionError(c, service.ErrCustomerNoURIEnabledNodes)
		return
	}
	result := strings.Join(content.Links, "\n") + "\n"
	if wantsPlainCustomerSubscription(c) {
		c.String(http.StatusOK, result)
		return
	}
	c.String(http.StatusOK, base64.StdEncoding.EncodeToString([]byte(result)))
}

func parsePositiveID(c *gin.Context, value string) (int, bool) {
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		if err == nil {
			err = errors.New("invalid id")
		}
		jsonMsg(c, "invalid id", err)
		return 0, false
	}
	return id, true
}

func buildCustomerSubscriptionURL(c *gin.Context, token string) string {
	path := buildCustomerSubscriptionPath(c, token)
	if publicBaseURL := strings.TrimSpace(os.Getenv("XUI_PUBLIC_SUB_BASE_URL")); publicBaseURL != "" {
		return strings.TrimRight(publicBaseURL, "/") + path
	}

	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	if host == "" {
		host = c.GetHeader("X-Real-IP")
	}
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}

func buildCustomerSubscriptionPath(c *gin.Context, token string) string {
	basePath := c.GetString("base_path")
	if basePath == "" {
		basePath = "/"
	}
	path := strings.TrimRight(basePath, "/") + "/customer-sub/" + token
	if strings.HasPrefix(path, "//") {
		path = strings.TrimPrefix(path, "/")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func writeCustomerSubscriptionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrCustomerNotFound):
		c.String(http.StatusNotFound, "subscription not found")
	case errors.Is(err, service.ErrCustomerDisabled),
		errors.Is(err, service.ErrCustomerExpired),
		errors.Is(err, service.ErrCustomerNoEnabledNodes),
		errors.Is(err, service.ErrCustomerNoURIEnabledNodes):
		writeEmptyCustomerSubscription(c)
	default:
		c.String(http.StatusBadRequest, err.Error())
	}
}

func writeEmptyCustomerSubscription(c *gin.Context) {
	setCustomerSubscriptionNoCacheHeaders(c)
	c.Header("Subscription-Userinfo", "upload=0; download=0; total=0; expire=1")
	c.Header("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte("Subscription Expired")))

	if strings.EqualFold(c.Query("format"), "clash") {
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", []byte(emptyCustomerClashSubscription()))
		return
	}

	body := "# subscription expired\n"
	if wantsPlainCustomerSubscription(c) {
		c.String(http.StatusOK, body)
		return
	}
	c.String(http.StatusOK, base64.StdEncoding.EncodeToString([]byte(body)))
}

func emptyCustomerClashSubscription() string {
	return "proxies: []\nproxy-groups:\n  - name: Proxy\n    type: select\n    proxies: []\nrules:\n  - MATCH,DIRECT\n"
}

func setCustomerSubscriptionNoCacheHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
}

func wantsPlainCustomerSubscription(c *gin.Context) bool {
	if c.Query("plain") == "1" || strings.EqualFold(c.Query("plain"), "true") {
		return true
	}
	return strings.Contains(strings.ToLower(c.GetHeader("User-Agent")), "shadowrocket")
}
