package eas

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
)

// provisionedPolicyTypes are the provisioned policy schemas this server can
// answer. Only the handshake is implemented: no policy is enforced beyond
// storing the acknowledged key, and remote wipe is never acted on
// (EAS_EVIDENCE.md section 2).
const (
	policyTypeEAS = "MS-EAS-Provisioning-WBXML"
	policyTypeWAP = "MS-WAP-Provisioning-XML"
)

// handleProvision completes the two-phase handshake: phase 1 issues a fresh
// policy key with permissive policy data; phase 2 confirms the client ack.
func (s *Server) handleProvision(ctx context.Context, sess Session, deviceID string, req *Node) (*Node, error) {
	resp := Element(pageProvision, tokProvision)
	device, err := GetDevice(ctx, s.Pool, sess, deviceID)
	if err != nil {
		return nil, err
	}
	policies := req.Child(tokPolicies)
	if policies == nil {
		// MS-ASPROV requires a Policies element; refusal without a key.
		resp.Add(Text(pageProvision, tokProvStatus, provProtocolError))
		return resp, nil
	}
	for _, child := range req.Children {
		if child.Page == 18 && child.Token == 0x16 {
			resp.Add(Element(18, 0x16, Text(18, 0x06, "1")))
		}
	}
	resp.Add(Text(pageProvision, tokProvStatus, provSuccess))
	responsePolicies := Element(pageProvision, tokPolicies)
	for _, policy := range policies.Children {
		if policy.Token != tokPolicy {
			continue
		}
		responsePolicies.Add(s.handlePolicy(ctx, sess, deviceID, device, policy))
	}
	resp.Add(responsePolicies)
	// Remote wipe can only appear as a policy inside the request; the loop
	// above ignores it and no wipe path exists anywhere in this package.
	return resp, nil
}

// handlePolicy answers one Policy element according to the handshake phase.
func (s *Server) handlePolicy(ctx context.Context, sess Session, deviceID string, device Device, policy *Node) *Node {
	resp := Element(pageProvision, tokPolicy)
	resp.Add(Text(pageProvision, tokPolicyType, policy.ChildText(tokPolicyType)))
	policyType := policy.ChildText(tokPolicyType)
	if policyType != policyTypeEAS {
		resp.Add(Text(pageProvision, tokProvStatus, provPolicyTypeUnsupported))
		return resp
	}
	// Any request that carries a RemoteWipe policy element is refused: the
	// transport must not accept device management obligations.
	if policy.Child(tokRemoteWipe) != nil {
		resp.Add(Text(pageProvision, tokProvStatus, provPolicyTypeUnsupported))
		return resp
	}
	clientKey := policy.ChildText(tokPolicyKey)
	switch clientKey {
	case "", "0":
		// Phase 1: issue a new positive key and permissive policy data. No
		// restriction is communicated beyond what keeps clients syncing.
		key, err := newPolicyKey()
		if err != nil {
			resp.Add(Text(pageProvision, tokProvStatus, provServerError))
			return resp
		}
		if err = SetPolicyKey(ctx, s.Pool, sess, deviceID, key, false); err != nil {
			resp.Add(Text(pageProvision, tokProvStatus, provServerError))
			return resp
		}
		resp.Add(Text(pageProvision, tokProvStatus, provSuccess))
		resp.Add(Text(pageProvision, tokPolicyKey, itoa(key)))
		if policyType == policyTypeEAS {
			resp.Add(Element(pageProvision, tokPData, permissiveEASDoc()))
		}
		return resp
	default:
		// Phase 2: the client acks the previously issued key.
		acked, err := strconvAtoi(clientKey)
		if err != nil || acked != device.PolicyKey || device.PolicyKey == 0 {
			// Wrong key: protocol error, device stays unprovisioned.
			resp.Add(Text(pageProvision, tokProvStatus, provProtocolError))
			return resp
		}
		if err = SetPolicyKey(ctx, s.Pool, sess, deviceID, acked, true); err != nil {
			resp.Add(Text(pageProvision, tokProvStatus, provServerError))
			return resp
		}
		resp.Add(Text(pageProvision, tokProvStatus, provSuccess))
		resp.Add(Text(pageProvision, tokPolicyKey, itoa(acked)))
		return resp
	}
}

// permissiveEASDoc returns an EASProvisionDoc that enables syncing: password
// policies off, storage allowed. No remote wipe element is ever emitted.
func permissiveEASDoc() *Node {
	doc := Element(pageProvision, tokEASDoc)
	doc.Add(Text(pageProvision, tokDevPwdEnabled, "0"))
	doc.Add(Text(pageProvision, 0x0F, "0")) // AlphanumericDevicePasswordRequired
	doc.Add(Text(pageProvision, 0x19, "0")) // DevicePasswordExpiration
	doc.Add(Text(pageProvision, 0x16, "0")) // MaxDevicePasswordFailedAttempts
	doc.Add(Text(pageProvision, 0x15, "0")) // MaxInactivityTimeDeviceLock
	doc.Add(Text(pageProvision, 0x1B, "1")) // AllowStorageCard
	doc.Add(Text(pageProvision, 0x1C, "1")) // AllowCamera
	doc.Add(Text(pageProvision, 0x1D, "0")) // RequireDeviceEncryption (off)
	return doc
}

// newPolicyKey mints an unsigned 32-bit key (MS-ASPROV PolicyKey).
func newPolicyKey() (int64, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	v := int64(binary.BigEndian.Uint32(b[:])) | 1
	if v <= 0 {
		return 0, errors.New("eas: policy key overflow")
	}
	return v, nil
}

// strconvAtoi avoids importing strconv in this file for one call site.
func strconvAtoi(s string) (int64, error) {
	if s == "" || strings.ContainsAny(s, "+-") {
		return 0, errors.New("eas: bad integer")
	}
	v, err := strconv.ParseUint(s, 10, 32)
	return int64(v), err
}
