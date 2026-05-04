package service

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	xuilogger "github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"
	"github.com/op/go-logging"
)

// 3x-ui logger must be initialised once before any code path that can
// log a warning, otherwise logger.Warningf panics on a nil logger.
var ensureLoggerOnce sync.Once

// setupTestDB wires a temp sqlite db so InboundService methods that
// touch the database (Create, Where, etc.) work end to end.
func setupTestDB(t *testing.T) {
	t.Helper()

	ensureLoggerOnce.Do(func() {
		xuilogger.InitLogger(logging.ERROR)
	})

	dbDir := t.TempDir()
	logDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	t.Setenv("XUI_LOG_FOLDER", logDir)

	if err := database.InitDB(filepath.Join(dbDir, "3x-ui.db")); err != nil {
		t.Fatalf("database.InitDB failed: %v", err)
	}
	t.Cleanup(func() {
		if err := database.CloseDB(); err != nil {
			t.Logf("database.CloseDB warning: %v", err)
		}
	})
}

// seedInboundWithDevices inserts an inbound whose settings json carries
// a single VLESS client with the given email and devices array. Returns
// the inbound id so the test can pass it to ensureDeviceStatRows.
func seedInboundWithDevices(t *testing.T, email string, devs []model.Device) int {
	t.Helper()

	type rawDev struct {
		Name   string `json:"name"`
		ID     string `json:"id"`
		Enable bool   `json:"enable"`
	}
	rawDevs := make([]rawDev, 0, len(devs))
	for _, d := range devs {
		rawDevs = append(rawDevs, rawDev{Name: d.Name, ID: d.ID, Enable: d.Enable})
	}
	settings := map[string]any{
		"clients": []map[string]any{
			{
				"email":   email,
				"id":      "parent-uuid-" + email,
				"enable":  true,
				"devices": rawDevs,
			},
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	in := &model.Inbound{
		Tag:      "inbound-test-" + email,
		Enable:   true,
		Protocol: model.VLESS,
		Port:     4321,
		Settings: string(raw),
	}
	if err := database.GetDB().Create(in).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	return in.Id
}

// TestEnsureDeviceStatRows_BackfillsMissing pins the fix for "device
// traffic vanishes from the parent client_traffics row": for a client
// configured with two devices but with no per-device rows in
// client_traffics yet (the exact state of user1 on the production VPS
// at investigation time), ensureDeviceStatRows must create one zero
// counter row per "<parent>-<device.Name>" so the next addClientTraffic
// pass can find them and increment up/down.
func TestEnsureDeviceStatRows_BackfillsMissing(t *testing.T) {
	setupTestDB(t)

	inboundId := seedInboundWithDevices(t, "user1", []model.Device{
		{Name: "pc", ID: "dev-pc", Enable: true},
		{Name: "phone", ID: "dev-phone", Enable: true},
	})

	// Mirror reality: parent row exists (created on AddInboundClient or
	// startup), per-device rows do not.
	if err := database.GetDB().Create(&xray.ClientTraffic{
		InboundId: inboundId,
		Email:     "user1",
		Enable:    true,
	}).Error; err != nil {
		t.Fatalf("seed parent ClientTraffic: %v", err)
	}

	svc := &InboundService{}
	if err := svc.ensureDeviceStatRows(database.GetDB()); err != nil {
		t.Fatalf("ensureDeviceStatRows: %v", err)
	}

	for _, want := range []string{"user1-pc", "user1-phone"} {
		var got xray.ClientTraffic
		err := database.GetDB().Where("email = ?", want).First(&got).Error
		if err != nil {
			t.Fatalf("expected device row %q after back-fill, got error: %v", want, err)
		}
		if got.InboundId != inboundId {
			t.Errorf("device row %q: InboundId = %d, want %d", want, got.InboundId, inboundId)
		}
		if got.Up != 0 || got.Down != 0 {
			t.Errorf("device row %q: expected zero Up/Down, got Up=%d Down=%d", want, got.Up, got.Down)
		}
	}
}

// TestEnsureDeviceStatRows_Idempotent makes sure a second call does
// not duplicate rows or fail on the unique-email constraint. The
// addClientTraffic→ensure→addClientTraffic loop runs once per traffic
// poll tick (default 10 s), so a regression that errors on the second
// pass would surface as a flood of "UNIQUE constraint failed" log
// warnings.
func TestEnsureDeviceStatRows_Idempotent(t *testing.T) {
	setupTestDB(t)

	inboundId := seedInboundWithDevices(t, "alice", []model.Device{
		{Name: "pc", ID: "dev-pc", Enable: true},
	})
	if err := database.GetDB().Create(&xray.ClientTraffic{
		InboundId: inboundId,
		Email:     "alice",
		Enable:    true,
	}).Error; err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	svc := &InboundService{}
	for i := 0; i < 3; i++ {
		if err := svc.ensureDeviceStatRows(database.GetDB()); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}

	var count int64
	if err := database.GetDB().Model(&xray.ClientTraffic{}).
		Where("email = ?", "alice-pc").
		Count(&count).Error; err != nil {
		t.Fatalf("count alice-pc: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 alice-pc row after 3 calls, got %d", count)
	}
}

// TestEnsureDeviceStatRows_SkipsLegacyClients pins that clients
// without a Devices array are NOT touched. For legacy single-device
// clients the parent row IS the device row; creating "<email>-..."
// shadows would double-count traffic and break the email-keyed
// matching used by addClientTraffic.
func TestEnsureDeviceStatRows_SkipsLegacyClients(t *testing.T) {
	setupTestDB(t)

	settings := map[string]any{
		"clients": []map[string]any{
			{"email": "bob", "id": "bob-uuid", "enable": true},
		},
	}
	raw, _ := json.Marshal(settings)
	in := &model.Inbound{
		Tag: "inbound-bob", Enable: true, Protocol: model.VLESS,
		Port: 4321, Settings: string(raw),
	}
	if err := database.GetDB().Create(in).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{
		InboundId: in.Id, Email: "bob", Enable: true,
	}).Error; err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	svc := &InboundService{}
	if err := svc.ensureDeviceStatRows(database.GetDB()); err != nil {
		t.Fatalf("ensureDeviceStatRows: %v", err)
	}

	var rows []xray.ClientTraffic
	if err := database.GetDB().Where("email LIKE ?", "bob%").Find(&rows).Error; err != nil {
		t.Fatalf("query bob rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Email != "bob" {
		t.Errorf("expected exactly 1 row 'bob' for legacy client, got %+v", rows)
	}
}
