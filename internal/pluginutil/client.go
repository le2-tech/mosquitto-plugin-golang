package pluginutil

// ClientInfo 保存客户端事件中常用的标识字段。
type ClientInfo struct {
	ClientID string
	Username string
	Peer     string
	Protocol string
}
