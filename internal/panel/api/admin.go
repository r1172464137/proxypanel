package api

import (
	"net/http"
	"strings"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/secrets"
)

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Overview(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, d)
}

func (s *Server) handleAdminAgents(w http.ResponseWriter, r *http.Request) {
	s.operationsAgents(w, r)
}

func (s *Server) handleAdminCoreInstances(w http.ResponseWriter, r *http.Request) {
	s.operationsCoreInstances(w, r)
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminPage(r, "servers")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.Store.ListAdminServersPage(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeAdminPage(s, w, r, "servers", p, page)
}

func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string     `json:"name"`
		Address   string     `json:"address"`
		Region    string     `json:"region"`
		Notes     string     `json:"notes"`
		ExpiresAt *time.Time `json:"expires_at"`
		CostMinor *int64     `json:"cost_minor"`
		Currency  string     `json:"currency"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		s.writeError(w, r, domain.Validation("name is required"))
		return
	}
	v, err := s.Store.CreateServer(r.Context(), &domain.Server{ID: newID(ids.Server), Name: in.Name,
		Address: in.Address, Region: in.Region, Notes: in.Notes, ExpiresAt: in.ExpiresAt,
		CostMinor: in.CostMinor, Currency: in.Currency})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "server.create", "server", v.ID, nil)
	s.writeData(w, http.StatusCreated, v)
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, v)
}

// serverPatchInput separates omitted fields from explicit nulls: `expires_at:
// null` and `cost_minor: null` clear the values instead of being ignored.
type serverPatchInput struct {
	Version   int64               `json:"version"`
	Name      *string             `json:"name"`
	Address   *string             `json:"address"`
	Region    *string             `json:"region"`
	Notes     *string             `json:"notes"`
	ExpiresAt nullable[time.Time] `json:"expires_at"`
	CostMinor nullable[int64]     `json:"cost_minor"`
	Currency  *string             `json:"currency"`
}

// applyServerPatch mutates the server row. Omitted fields stay untouched and an
// explicit null clears the nullable ones.
func applyServerPatch(v *domain.Server, in *serverPatchInput) error {
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return domain.Validation("name must not be empty").
				WithFieldErrors(map[string]string{"name": "must not be empty"})
		}
		v.Name = *in.Name
	}
	if in.Address != nil {
		v.Address = *in.Address
	}
	if in.Region != nil {
		v.Region = *in.Region
	}
	if in.Notes != nil {
		v.Notes = *in.Notes
	}
	if in.ExpiresAt.Set {
		if in.ExpiresAt.Null {
			v.ExpiresAt = nil
		} else {
			expiry := in.ExpiresAt.Value.UTC()
			v.ExpiresAt = &expiry
		}
	}
	if in.CostMinor.Set {
		if in.CostMinor.Null {
			v.CostMinor = nil
		} else {
			if in.CostMinor.Value < 0 {
				return domain.Validation("cost_minor must not be negative").
					WithFieldErrors(map[string]string{"cost_minor": "must not be negative"})
			}
			cost := in.CostMinor.Value
			v.CostMinor = &cost
		}
	}
	if in.Currency != nil {
		v.Currency = *in.Currency
	}
	return nil
}

func (s *Server) handlePatchServer(w http.ResponseWriter, r *http.Request) {
	var in serverPatchInput
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := requireVersion(in.Version); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.Store.UpdateServer(r.Context(), r.PathValue("id"), in.Version, func(v *domain.Server) error {
		return applyServerPatch(v, &in)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "server.update", "server", v.ID, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nodes, err := s.Store.ListNodes(r.Context(), id, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(nodes) > 0 {
		s.writeError(w, r, domain.Conflict(domain.CodeNodeInUse, "server still hosts %d node(s)", len(nodes)))
		return
	}
	v, err := s.Store.UpdateServer(r.Context(), id, 0, func(sv *domain.Server) error { sv.Archived = true; return nil })
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "server.archive", "server", id, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleRegistrationCode(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	code := secrets.NewToken(24)
	expires := time.Now().UTC().Add(15 * time.Minute)
	id, err := s.Store.CreateRegistrationCode(r.Context(), serverID, secrets.HashToken(code), expires,
		memberFrom(r.Context()).Username)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "agent.registration_code", "server", serverID, nil)
	command, available, reason := s.agentInstallCommand(code)
	s.writeData(w, http.StatusCreated, map[string]any{
		"id": id, "code": code, "expires_at": expires,
		"installer_available": available, "install_command": command, "installer_reason": reason,
	})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminPage(r, "nodes")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.Store.ListAdminNodesPage(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeAdminPage(s, w, r, "nodes", p, page)
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		ServerID string `json:"server_id"`
		Enabled  *bool  `json:"enabled"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		// ExternalPort 可选：客户端连接的端口，用于「对外 443 → 实际 20101」。
		// 不传或传 0 表示与 Port 相同。
		ExternalPort     int           `json:"external_port"`
		Listen           string        `json:"listen"`
		Protocol         string        `json:"protocol"`
		Flow             string        `json:"flow"`
		Cipher           string        `json:"cipher"`
		ServerKey        string        `json:"server_key"`
		StreamSettings   domain.Object `json:"stream_settings"`
		ProtocolSettings domain.Object `json:"protocol_settings"`
		ClientSettings   domain.Object `json:"client_settings"`
		Sniffing         domain.Object `json:"sniffing"`
		Allocate         domain.Object `json:"allocate"`
		ExternalProxy    []any         `json:"external_proxy"`
		SortOrder        int           `json:"sort_order"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if _, err := s.Store.GetServer(r.Context(), in.ServerID); err != nil {
		s.writeError(w, r, err)
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	listen := in.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	stream, ps, key, issues := protocol.Sanitize(protocol.Node{Protocol: in.Protocol, Flow: in.Flow,
		Cipher: in.Cipher, Listen: listen, Host: in.Host, Port: in.Port, ExternalPort: in.ExternalPort,
		StreamSettings:   in.StreamSettings,
		ProtocolSettings: in.ProtocolSettings, ClientSettings: in.ClientSettings, ServerKey: in.ServerKey})
	if len(issues) > 0 {
		s.writeError(w, r, fieldErrors(issues))
		return
	}
	sniffing, allocate, externalProxy, inboundIssues := protocol.SanitizeInbound(in.Sniffing, in.Allocate, in.ExternalProxy)
	if len(inboundIssues) > 0 {
		s.writeError(w, r, fieldErrors(inboundIssues))
		return
	}
	node := &domain.Node{ID: newID(ids.Node), ServerID: in.ServerID, Name: in.Name, Enabled: enabled,
		Host: in.Host, Port: in.Port, ExternalPort: in.ExternalPort, Listen: listen,
		Protocol: in.Protocol, Flow: in.Flow, Cipher: in.Cipher,
		StreamSettings: stream, ProtocolSettings: ps, ClientSettings: in.ClientSettings,
		Sniffing: sniffing, Allocate: allocate, ExternalProxy: externalProxy,
		SortOrder: in.SortOrder, ServerKey: key}
	if err := ensureRealityKeys(node); err != nil {
		s.writeError(w, r, err)
		return
	}
	if issues := protocol.Validate(protocolNode(node)); len(issues) > 0 {
		s.writeError(w, r, fieldErrors(issues))
		return
	}
	created, err := s.Store.CreateNode(r.Context(), node, newID(ids.CoreInstance))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "node.create", "node", created.ID, nil)
	s.writeData(w, http.StatusCreated, created)
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.GetNode(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handlePatchNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version  int64   `json:"version"`
		Name     *string `json:"name"`
		ServerID *string `json:"server_id"`
		Enabled  *bool   `json:"enabled"`
		Host     *string `json:"host"`
		Port     *int    `json:"port"`
		// 传 0 表示清除对外端口（退回与 port 相同）。
		ExternalPort     *int          `json:"external_port"`
		Listen           *string       `json:"listen"`
		Protocol         *string       `json:"protocol"`
		Flow             *string       `json:"flow"`
		Cipher           *string       `json:"cipher"`
		ServerKey        *string       `json:"server_key"`
		StreamSettings   domain.Object `json:"stream_settings"`
		ProtocolSettings domain.Object `json:"protocol_settings"`
		ClientSettings   domain.Object `json:"client_settings"`
		Sniffing         domain.Object `json:"sniffing"`
		Allocate         domain.Object `json:"allocate"`
		ExternalProxy    []any         `json:"external_proxy"`
		SortOrder        *int          `json:"sort_order"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := requireVersion(in.Version); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.Store.UpdateNode(r.Context(), r.PathValue("id"), in.Version, func(n *domain.Node) error {
		if in.Name != nil {
			n.Name = *in.Name
		}
		if in.ServerID != nil {
			n.ServerID = *in.ServerID
		}
		if in.Enabled != nil {
			n.Enabled = *in.Enabled
		}
		if in.Host != nil {
			n.Host = *in.Host
		}
		if in.Port != nil {
			n.Port = *in.Port
		}
		if in.ExternalPort != nil {
			n.ExternalPort = *in.ExternalPort
		}
		if in.Listen != nil {
			n.Listen = *in.Listen
		}
		if in.Protocol != nil {
			n.Protocol = *in.Protocol
		}
		if in.Flow != nil {
			n.Flow = *in.Flow
		}
		if in.Cipher != nil {
			n.Cipher = *in.Cipher
		}
		if in.SortOrder != nil {
			n.SortOrder = *in.SortOrder
		}
		if in.StreamSettings != nil {
			n.StreamSettings = in.StreamSettings
		}
		if in.ProtocolSettings != nil {
			n.ProtocolSettings = in.ProtocolSettings
		}
		if in.ClientSettings != nil {
			n.ClientSettings = in.ClientSettings
		}
		if in.ServerKey != nil {
			n.ServerKey = *in.ServerKey
		}
		// Assign before validating: SanitizeInbound has to see the submitted
		// values, not the row loaded from the database.
		if in.Sniffing != nil {
			n.Sniffing = in.Sniffing
		}
		if in.Allocate != nil {
			n.Allocate = in.Allocate
		}
		if in.ExternalProxy != nil {
			n.ExternalProxy = in.ExternalProxy
		}
		if in.Sniffing != nil || in.Allocate != nil || in.ExternalProxy != nil {
			sniffing, allocate, externalProxy, inboundIssues := protocol.SanitizeInbound(n.Sniffing, n.Allocate, n.ExternalProxy)
			if len(inboundIssues) > 0 {
				return fieldErrors(inboundIssues)
			}
			n.Sniffing, n.Allocate, n.ExternalProxy = sniffing, allocate, externalProxy
		}
		stream, ps, key, issues := protocol.Sanitize(protocolNode(n))
		if len(issues) > 0 {
			return fieldErrors(issues)
		}
		n.StreamSettings, n.ProtocolSettings, n.ServerKey = stream, ps, key
		if err := ensureRealityKeys(n); err != nil {
			return err
		}
		if issues := protocol.Validate(protocolNode(n)); len(issues) > 0 {
			return fieldErrors(issues)
		}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// The revision is published by UpdateNode in the same transaction as the
	// change, so no separate (failable) bump happens here.
	s.audit(r, "node.update", "node", v.ID, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.DeleteNode(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// DeleteNode archives through UpdateNode, which publishes the revision in
	// its own transaction.
	s.audit(r, "node.archive", "node", v.ID, nil)
	s.writeData(w, http.StatusOK, v)
}
