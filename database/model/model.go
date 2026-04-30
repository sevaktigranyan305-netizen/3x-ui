// Package model defines the database models and data structures used by the 3x-ui panel.
package model

import (
	"fmt"

	"github.com/mhsanaei/3x-ui/v2/util/json_util"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// Protocol represents the protocol type for Xray inbounds.
type Protocol string

// Protocol constants for different Xray inbound protocols
const (
	VMESS       Protocol = "vmess"
	VLESS       Protocol = "vless"
	Tunnel      Protocol = "tunnel"
	HTTP        Protocol = "http"
	Trojan      Protocol = "trojan"
	Shadowsocks Protocol = "shadowsocks"
	Mixed       Protocol = "mixed"
	WireGuard   Protocol = "wireguard"
	// UI stores Hysteria v1 and v2 both as "hysteria" and uses
	// settings.version to discriminate. Imports from outside the panel
	// can carry the literal "hysteria2" string, so IsHysteria below
	// accepts both.
	Hysteria  Protocol = "hysteria"
	Hysteria2 Protocol = "hysteria2"
)

// IsHysteria returns true for both "hysteria" and "hysteria2".
// Use instead of a bare ==model.Hysteria check: a v2 inbound stored
// with the literal v2 string would otherwise fall through (#4081).
func IsHysteria(p Protocol) bool {
	return p == Hysteria || p == Hysteria2
}

// User represents a user account in the 3x-ui panel.
type User struct {
	Id       int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Inbound represents an Xray inbound configuration with traffic statistics and settings.
type Inbound struct {
	Id                   int                  `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`                                                    // Unique identifier
	UserId               int                  `json:"-"`                                                                                               // Associated user ID
	Up                   int64                `json:"up" form:"up"`                                                                                    // Upload traffic in bytes
	Down                 int64                `json:"down" form:"down"`                                                                                // Download traffic in bytes
	Total                int64                `json:"total" form:"total"`                                                                              // Total traffic limit in bytes
	AllTime              int64                `json:"allTime" form:"allTime" gorm:"default:0"`                                                         // All-time traffic usage
	Remark               string               `json:"remark" form:"remark"`                                                                            // Human-readable remark
	Enable               bool                 `json:"enable" form:"enable" gorm:"index:idx_enable_traffic_reset,priority:1"`                           // Whether the inbound is enabled
	ExpiryTime           int64                `json:"expiryTime" form:"expiryTime"`                                                                    // Expiration timestamp
	TrafficReset         string               `json:"trafficReset" form:"trafficReset" gorm:"default:never;index:idx_enable_traffic_reset,priority:2"` // Traffic reset schedule
	LastTrafficResetTime int64                `json:"lastTrafficResetTime" form:"lastTrafficResetTime" gorm:"default:0"`                               // Last traffic reset timestamp
	ClientStats          []xray.ClientTraffic `gorm:"foreignKey:InboundId;references:Id" json:"clientStats" form:"clientStats"`                        // Client traffic statistics

	// Xray configuration fields
	Listen         string   `json:"listen" form:"listen"`
	Port           int      `json:"port" form:"port"`
	Protocol       Protocol `json:"protocol" form:"protocol"`
	Settings       string   `json:"settings" form:"settings"`
	StreamSettings string   `json:"streamSettings" form:"streamSettings"`
	Tag            string   `json:"tag" form:"tag" gorm:"unique"`
	Sniffing       string   `json:"sniffing" form:"sniffing"`

	// VirtualnetAssignments is a (uuid -> ip) snapshot of the panel's
	// IPAM allocation for this inbound's L3 virtualNetwork. Populated
	// at API-response time only (gorm:"-" so it never touches the DB)
	// so the panel UI's link generator can append &vnetIp= to the
	// VLESS link it shows in the "view link" / QR modals. Empty when
	// the inbound is not VLESS or virtualNetwork is disabled.
	VirtualnetAssignments map[string]string `json:"virtualNetworkAssignments,omitempty" gorm:"-"`
}

// OutboundTraffics tracks traffic statistics for Xray outbound connections.
type OutboundTraffics struct {
	Id    int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Tag   string `json:"tag" form:"tag" gorm:"unique"`
	Up    int64  `json:"up" form:"up" gorm:"default:0"`
	Down  int64  `json:"down" form:"down" gorm:"default:0"`
	Total int64  `json:"total" form:"total" gorm:"default:0"`
}

// InboundClientIps stores IP addresses associated with inbound clients for access control.
type InboundClientIps struct {
	Id          int    `json:"id" gorm:"primaryKey;autoIncrement"`
	ClientEmail string `json:"clientEmail" form:"clientEmail" gorm:"unique"`
	Ips         string `json:"ips" form:"ips"`
}

// HistoryOfSeeders tracks which database seeders have been executed to prevent re-running.
type HistoryOfSeeders struct {
	Id         int    `json:"id" gorm:"primaryKey;autoIncrement"`
	SeederName string `json:"seederName"`
}

// GenXrayInboundConfig generates an Xray inbound configuration from the Inbound model.
func (i *Inbound) GenXrayInboundConfig() *xray.InboundConfig {
	listen := i.Listen
	// Default to 0.0.0.0 (all interfaces) when listen is empty
	// This ensures proper dual-stack IPv4/IPv6 binding in systems where bindv6only=0
	if listen == "" {
		listen = "0.0.0.0"
	}
	listen = fmt.Sprintf("\"%v\"", listen)
	return &xray.InboundConfig{
		Listen:         json_util.RawMessage(listen),
		Port:           i.Port,
		Protocol:       string(i.Protocol),
		Settings:       json_util.RawMessage(i.Settings),
		StreamSettings: json_util.RawMessage(i.StreamSettings),
		Tag:            i.Tag,
		Sniffing:       json_util.RawMessage(i.Sniffing),
	}
}

// Setting stores key-value configuration settings for the 3x-ui panel.
type Setting struct {
	Id    int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Key   string `json:"key" form:"key"`
	Value string `json:"value" form:"value"`
}

type CustomGeoResource struct {
	Id            int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Type          string `json:"type" gorm:"not null;uniqueIndex:idx_custom_geo_type_alias;column:geo_type"`
	Alias         string `json:"alias" gorm:"not null;uniqueIndex:idx_custom_geo_type_alias"`
	Url           string `json:"url" gorm:"not null"`
	LocalPath     string `json:"localPath" gorm:"column:local_path"`
	LastUpdatedAt int64  `json:"lastUpdatedAt" gorm:"default:0;column:last_updated_at"`
	LastModified  string `json:"lastModified" gorm:"column:last_modified"`
	CreatedAt     int64  `json:"createdAt" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt     int64  `json:"updatedAt" gorm:"autoUpdateTime;column:updated_at"`
}

// Client represents a client configuration for Xray inbounds with traffic limits and settings.
//
// A client may carry one or more Devices. When Devices is non-empty, the client
// becomes a logical container ("user") and its Devices are what xray actually sees:
// each device is emitted into xray's settings.clients[] as an independent entry whose
// xray-side email is "<Client.Email>-<Device.Name>". Per-device traffic stats are
// keyed by that derived email; parent-level limits (TotalGB, ExpiryTime) apply to
// the SUM of per-device usage and are enforced by the panel by toggling Enable on
// every device in the client at once.
//
// When Devices is empty the panel falls back to legacy single-device behaviour
// using the client's own ID/Flow as before — this keeps existing inbounds working
// during rollout, but new clients should always carry at least one device.
type Client struct {
	ID         string   `json:"id,omitempty"`                 // Unique client identifier (legacy single-device mode)
	Security   string   `json:"security"`                     // Security method (e.g., "auto", "aes-128-gcm")
	Password   string   `json:"password,omitempty"`           // Client password
	Flow       string   `json:"flow,omitempty"`               // Flow control (XTLS), inherited by devices that omit it
	Auth       string   `json:"auth,omitempty"`               // Auth password (Hysteria)
	Email      string   `json:"email"`                        // Client email identifier (the "user" name)
	LimitIP    int      `json:"limitIp"`                      // IP limit for this client (inherited by devices)
	TotalGB    int64    `json:"totalGB" form:"totalGB"`       // Total traffic limit in GB (sum across all devices)
	ExpiryTime int64    `json:"expiryTime" form:"expiryTime"` // Expiration timestamp (applies to the whole client)
	Enable     bool     `json:"enable" form:"enable"`         // Master enable for the client
	TgID       int64    `json:"tgId" form:"tgId"`             // Telegram user ID for notifications
	SubID      string   `json:"subId" form:"subId"`           // Subscription identifier
	Comment    string   `json:"comment" form:"comment"`       // Client comment
	Reset      int      `json:"reset" form:"reset"`           // Reset period in days
	CreatedAt  int64    `json:"created_at,omitempty"`         // Creation timestamp
	UpdatedAt  int64    `json:"updated_at,omitempty"`         // Last update timestamp
	Devices    []Device `json:"devices,omitempty"`            // Devices belonging to this client; each becomes a flat xray client
}

// Device represents one physical device of a Client. Each device gets its own UUID
// and appears to xray as an independent client whose email is "<Client.Email>-<Device.Name>".
//
// Per-device fields:
//   - Name: free-form label, must be non-empty and unique within the client. Used
//     to derive the xray-side email; restricted to characters safe in xray emails
//     (validated at write time, not here).
//   - ID: the device's UUID. Distinct UUIDs are what let virtualnet IPAM hand out
//     distinct sequential IPs to phone/pc/mac of the same user.
//   - Flow / LimitIP: optional per-device overrides; if zero/empty, parent values apply.
//   - Enable: per-device toggle; effective enable = Client.Enable && Device.Enable.
//
// Traffic limit and expiry intentionally live on the parent Client, not on Device:
// the panel sums per-device traffic (each device has its own stats row keyed by the
// derived email) and disables the entire client when the parent limit is hit.
type Device struct {
	Name      string `json:"name"`                 // Device label (e.g. "pc", "phone", "abcd")
	ID        string `json:"id"`                   // Device UUID
	Flow      string `json:"flow,omitempty"`       // Optional per-device flow override
	LimitIP   int    `json:"limitIp,omitempty"`    // Optional per-device IP limit override
	Enable    bool   `json:"enable" form:"enable"` // Per-device enable toggle
	Comment   string `json:"comment,omitempty"`    // Optional per-device note
	CreatedAt int64  `json:"created_at,omitempty"` // Creation timestamp
	UpdatedAt int64  `json:"updated_at,omitempty"` // Last update timestamp
}
