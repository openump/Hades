//go:build opcua_mock

// Package opcuamock is disposable software hardware, never a production server.
package opcuamock

import (
	"encoding/json"
	"fmt"
	"github.com/awcullen/opcua/server"
	"github.com/awcullen/opcua/ua"
	"github.com/openump/Hades/internal/opcuapilot"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Compilation without the reviewed fixture-only source patch must fail.
const loopbackPatchRequired = server.OpenUMPLoopbackOnly

func New(endpoint, pki, fault string, ledger io.Writer, pad bool) (*server.Server, error) {
	if err := opcuapilot.ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	if _, err := opcuapilot.Certificate(filepath.Join(pki, "server.crt"), opcuapilot.ServerURI); err != nil {
		return nil, err
	}
	trusted, err := opcuapilot.Certificate(filepath.Join(pki, "client.crt"), opcuapilot.ClientURI)
	if err != nil {
		return nil, err
	}
	srv, err := server.New(ua.ApplicationDescription{ApplicationURI: opcuapilot.ServerURI, ProductURI: opcuapilot.NamespaceURI, ApplicationName: ua.LocalizedText{Text: "OpenUMP mock v1"}, ApplicationType: ua.ApplicationTypeServer, DiscoveryURLs: []string{endpoint}}, filepath.Join(pki, "server.crt"), filepath.Join(pki, "server.key"), endpoint,
		server.WithSecurityPolicyNone(false), server.WithAnonymousIdentity(false), server.WithTrustedCertificatesPaths(filepath.Join(pki, "client.crt"), ""), server.WithMaxSessionCount(4), server.WithMaxSubscriptionCount(1), server.WithMaxWorkerThreads(4),
		server.WithAuthenticateX509IdentityFunc(func(identity ua.X509Identity, uri, endpoint string) error {
			if uri != opcuapilot.ClientURI || !opcuapilot.SameIdentity([]byte(identity.Certificate), trusted) {
				return ua.BadUserAccessDenied
			}
			return nil
		}),
		server.WithGetRolesFunc(func(identity any, uri, endpoint string) ([]ua.NodeID, error) {
			if _, ok := identity.(ua.X509Identity); !ok {
				return nil, ua.BadUserAccessDenied
			}
			return []ua.NodeID{ua.ObjectIDWellKnownRoleObserver}, nil
		}),
		server.WithRolePermissions([]ua.RolePermissionType{{RoleID: ua.ObjectIDWellKnownRoleObserver, Permissions: ua.PermissionTypeBrowse | ua.PermissionTypeRead}}))
	if err != nil {
		return nil, err
	}
	nm := srv.NamespaceManager()
	if pad {
		nm.Add("urn:openump:padding")
	}
	index := nm.Add(opcuapilot.NamespaceURI)
	var counter atomic.Uint32
	for _, signal := range opcuapilot.Signals {
		node := server.NewVariableNode(srv, ua.NodeIDString{NamespaceIndex: index, ID: signal.Node}, ua.QualifiedName{NamespaceIndex: index, Name: signal.Node}, ua.LocalizedText{Text: signal.Name}, ua.LocalizedText{}, nil, nil, ua.NewDataValue(nil, ua.BadWaitingForInitialData, time.Time{}, 0, time.Time{}, 0), signal.Type, ua.ValueRankScalar, nil, ua.AccessLevelsCurrentRead, 1000, false, nil)
		node.SetReadValueHandler(func(_ *server.Session, _ ua.ReadValueID) ua.DataValue {
			mode := "good"
			if fault != "" {
				raw, err := os.ReadFile(fault)
				if err != nil {
					mode = "bad"
				} else {
					mode = string(raw)
				}
			}
			status := ua.Good
			var value any = float64(67.2)
			if signal.Name == "cycle_counter" {
				value = counter.Add(1)
			}
			if signal.Name == "machine_state" {
				value = uint16(1)
			}
			switch mode {
			case "good", "no-source-time", "no-times":
			case "uncertain":
				status = ua.UncertainLastUsableValue
			case "missing":
				status = ua.BadNodeIDUnknown
			case "delay":
				time.Sleep(700 * time.Millisecond)
			case "wrong-type":
				value = "wrong-type"
			default:
				status = ua.BadSensorFailure
			}
			source := time.Now().UTC().Truncate(time.Millisecond)
			serverTime := source
			if mode == "no-source-time" || mode == "no-times" {
				source = time.Time{}
			}
			if mode == "no-times" {
				serverTime = time.Time{}
			}
			result := ua.NewDataValue(value, status, source, 0, serverTime, 0)
			raw, _ := json.Marshal(map[string]any{"node": signal.Node, "value": value, "status_code": uint32(status), "source_timestamp": source, "server_timestamp": serverTime, "mode": mode})
			if ledger != nil {
				if _, err := fmt.Fprintln(ledger, string(raw)); err != nil {
					return ua.NewDataValue(nil, ua.BadInternalError, source, 0, serverTime, 0)
				}
			}
			return result
		})
		if err := nm.AddNode(node); err != nil {
			return nil, err
		}
	}
	private := server.NewVariableNode(srv, ua.NodeIDString{NamespaceIndex: index, ID: "Private"}, ua.QualifiedName{NamespaceIndex: index, Name: "Private"}, ua.LocalizedText{Text: "Private"}, ua.LocalizedText{}, []ua.RolePermissionType{}, nil, ua.NewDataValue(uint32(99), ua.Good, time.Now(), 0, time.Now(), 0), ua.DataTypeIDUInt32, ua.ValueRankScalar, nil, ua.AccessLevelsCurrentRead, 1000, false, nil)
	if err := nm.AddNode(private); err != nil {
		return nil, err
	}
	return srv, nil
}
