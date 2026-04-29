package sub

import "github.com/mhsanaei/3x-ui/v2/database/model"

// expandClientsForSubscription returns the flat list of clients that should
// produce one subscription link each.
//
// A parent client without devices is returned unchanged (legacy behaviour).
// A parent client with one or more devices is replaced by N synthetic clients,
// one per enabled device. Each synthetic client carries:
//
//   - Email = "<parent.Email>-<device.Name>"
//   - ID    = device.ID (the per-device UUID)
//   - Flow  = device.Flow if non-empty, else parent's
//
// All other parent fields (SubID, Enable gating, Comment, expiry/total) are
// inherited verbatim so the subscription endpoint's existing per-client filter
// logic ("client.Enable && client.SubID == subId" etc.) continues to apply
// uniformly to every device.
//
// The function never panics on malformed input — devices missing Name or ID are
// skipped, and disabled devices (device.Enable == false) are skipped too. The
// returned slice keeps device order so the order of links in the subscription
// matches the order the user added devices in.
func expandClientsForSubscription(clients []model.Client) []model.Client {
	out := make([]model.Client, 0, len(clients))
	for i := range clients {
		c := clients[i]
		if len(c.Devices) == 0 {
			out = append(out, c)
			continue
		}
		for _, d := range c.Devices {
			if d.Name == "" || d.ID == "" || !d.Enable {
				continue
			}
			synth := c
			synth.Devices = nil
			synth.Email = c.Email + "-" + d.Name
			synth.ID = d.ID
			if d.Flow != "" {
				synth.Flow = d.Flow
			}
			if d.LimitIP > 0 {
				synth.LimitIP = d.LimitIP
			}
			out = append(out, synth)
		}
	}
	return out
}
