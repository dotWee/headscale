package hscontrol

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/types/change"
	"github.com/rs/zerolog/log"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

const vipServicesPath = "/vip-services"

var (
	errC2NNodeUnavailable = errors.New("node is not connected for c2n")
	errC2NUnknownToken    = errors.New("unknown c2n token")
	errC2NWrongNode       = errors.New("c2n response came from the wrong node")
)

type c2nPendingResponse struct {
	nodeKey key.NodePublic
	respCh  chan *http.Response
}

type c2nManager struct {
	mu      sync.Mutex
	pending map[string]c2nPendingResponse
}

func newC2NManager() *c2nManager {
	return &c2nManager{pending: make(map[string]c2nPendingResponse)}
}

func (m *c2nManager) add(token string, pending c2nPendingResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[token] = pending
}

func (m *c2nManager) take(token string) (c2nPendingResponse, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pending, ok := m.pending[token]
	if ok {
		delete(m.pending, token)
	}

	return pending, ok
}

func (m *c2nManager) delete(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, token)
}

func (h *Headscale) SendC2N(ctx context.Context, nodeKey key.NodePublic, req *http.Request) (*http.Response, error) {
	if h.mapBatcher == nil {
		return nil, errC2NNodeUnavailable
	}

	node, ok := h.state.GetNodeByNodeKey(nodeKey)
	if !ok || !h.mapBatcher.IsConnected(node.ID()) {
		return nil, errC2NNodeUnavailable
	}

	var payload bytes.Buffer
	if err := req.Write(&payload); err != nil {
		return nil, fmt.Errorf("writing c2n request payload: %w", err)
	}

	token, err := newC2NToken()
	if err != nil {
		return nil, fmt.Errorf("generating c2n token: %w", err)
	}

	respCh := make(chan *http.Response, 1)
	h.c2n.add(token, c2nPendingResponse{
		nodeKey: nodeKey,
		respCh:  respCh,
	})
	defer h.c2n.delete(token)

	replyURL := strings.TrimSuffix(h.cfg.ServerURL, "/") + "/machine/c2n/" + token
	h.Change(change.C2N(node.ID(), &tailcfg.PingRequest{
		URL:        replyURL,
		URLIsNoise: true,
		Log:        true,
		Types:      "c2n",
		Payload:    payload.Bytes(),
	}))

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-respCh:
		return resp, nil
	}
}

func (h *Headscale) maybeRefreshVIPServices(nodeID types.NodeID) {
	if !h.state.ServiceMetadataNeedsRefresh(nodeID) || !h.state.BeginServiceRefresh(nodeID) {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := h.refreshVIPServices(ctx, nodeID); err != nil {
			h.state.MarkServiceRefreshFailed(nodeID)
			log.Warn().
				Err(err).
				Uint64("node.id", nodeID.Uint64()).
				Msg("failed to refresh VIP service metadata")
		}
	}()
}

func (h *Headscale) refreshVIPServices(ctx context.Context, nodeID types.NodeID) error {
	node, ok := h.state.GetNodeByID(nodeID)
	if !ok {
		return errServeNodeUnavailable
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://c2n"+vipServicesPath, nil)
	if err != nil {
		return fmt.Errorf("creating c2n vip-services request: %w", err)
	}

	resp, err := h.SendC2N(ctx, node.NodeKey(), req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("c2n vip-services returned %s", resp.Status)
	}

	var vipResp tailcfg.C2NVIPServicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&vipResp); err != nil {
		return fmt.Errorf("decoding c2n vip-services response: %w", err)
	}
	if vipResp.ServicesHash == "" {
		vipResp.ServicesHash = node.Hostinfo().ServicesHash()
	}

	ch, err := h.state.SetVIPServices(nodeID, &vipResp)
	if err != nil {
		return err
	}
	if !ch.IsEmpty() {
		h.Change(ch)
	}

	return nil
}

func (ns *noiseServer) C2NResponseHandler(writer http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		httpError(writer, errMethodNotAllowed)
		return
	}

	token, err := urlParam[string](req, "token")
	if err != nil {
		httpError(writer, NewHTTPError(http.StatusBadRequest, "missing c2n token", err))
		return
	}

	pending, ok := ns.headscale.c2n.take(token)
	if !ok {
		httpError(writer, NewHTTPError(http.StatusNotFound, "unknown c2n token", errC2NUnknownToken))
		return
	}
	if pending.nodeKey != ns.nodeKey {
		httpError(writer, NewHTTPError(http.StatusForbidden, "node key does not match c2n token", errC2NWrongNode))
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(req.Body), nil)
	if err != nil {
		httpError(writer, NewHTTPError(http.StatusBadRequest, "invalid c2n response", err))
		return
	}

	select {
	case pending.respCh <- resp:
	default:
		resp.Body.Close()
		httpError(writer, NewHTTPError(http.StatusConflict, "c2n response channel already consumed", errors.New("duplicate c2n response")))
		return
	}

	writer.WriteHeader(http.StatusNoContent)
}

func newC2NToken() (string, error) {
	var buf [10]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf[:]), nil
}
