package etcdhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap/zaptest"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/raft/v3"
	"go.etcd.io/etcd/server/v3/auth"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver"
	betesting "go.etcd.io/etcd/server/v3/mvcc/backend/testing"
)

type fakeHealthServer struct {
	fakeServer
	health    string
	apiError  error
	authStore auth.AuthStore
}

func (s *fakeHealthServer) Range(_ context.Context, _ *pb.RangeRequest) (*pb.RangeResponse, error) {
	return nil, s.apiError
}

func (s *fakeHealthServer) Config() config.ServerConfig {
	return config.ServerConfig{}
}

func (s *fakeHealthServer) Leader() types.ID {
	if s.health == "true" {
		return 1
	}
	return types.ID(raft.None)
}
func (s *fakeHealthServer) Do(_ context.Context, _ pb.Request) (etcdserver.Response, error) {
	if s.health == "true" {
		return etcdserver.Response{}, nil
	}
	return etcdserver.Response{}, fmt.Errorf("fail health check")
}
func (s *fakeHealthServer) AuthStore() auth.AuthStore   { return s.authStore }
func (s *fakeHealthServer) ClientCertAuthEnabled() bool { return false }

func TestHealthHandler(t *testing.T) {
	// define the input and expected output
	// input: alarms, and healthCheckURL
	tests := []struct {
		name           string
		alarms         []*pb.AlarmMember
		healthCheckURL string
		apiError       error

		expectStatusCode int
		expectHealth     string
	}{
		{
			name:             "Healthy if no alarm",
			alarms:           []*pb.AlarmMember{},
			healthCheckURL:   "/health",
			expectStatusCode: http.StatusOK,
			expectHealth:     "true",
		},
		{
			name:             "Unhealthy if NOSPACE alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/health",
			expectStatusCode: http.StatusServiceUnavailable,
			expectHealth:     "false",
		},
		{
			name:             "Healthy if NOSPACE alarm is on and excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/health?exclude=NOSPACE",
			expectStatusCode: http.StatusOK,
			expectHealth:     "true",
		},
		{
			name:             "Healthy if NOSPACE alarm is excluded",
			alarms:           []*pb.AlarmMember{},
			healthCheckURL:   "/health?exclude=NOSPACE",
			expectStatusCode: http.StatusOK,
			expectHealth:     "true",
		},
		{
			name:             "Healthy if multiple NOSPACE alarms are on and excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(1), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(2), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(3), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/health?exclude=NOSPACE",
			expectStatusCode: http.StatusOK,
			expectHealth:     "true",
		},
		{
			name:             "Unhealthy if NOSPACE alarms is excluded and CORRUPT is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(1), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/health?exclude=NOSPACE",
			expectStatusCode: http.StatusServiceUnavailable,
			expectHealth:     "false",
		},
		{
			name:             "Unhealthy if both NOSPACE and CORRUPT are on and excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(1), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/health?exclude=NOSPACE&exclude=CORRUPT",
			expectStatusCode: http.StatusOK,
			expectHealth:     "true",
		},
		{
			name:             "Unhealthy if api is not available",
			healthCheckURL:   "/health",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusServiceUnavailable,
			expectHealth:     "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			lg := zaptest.NewLogger(t)
			be, _ := betesting.NewDefaultTmpBackend(t)
			defer betesting.Close(t, be)
			HandleHealth(zaptest.NewLogger(t), mux, &fakeHealthServer{
				fakeServer: fakeServer{alarms: tt.alarms},
				health:     tt.expectHealth,
				apiError:   tt.apiError,
				authStore:  auth.NewAuthStore(lg, be, nil, 0),
			})
			ts := httptest.NewServer(mux)
			defer ts.Close()

			res, err := ts.Client().Do(&http.Request{Method: http.MethodGet, URL: testutil.MustNewURL(t, ts.URL+tt.healthCheckURL)})
			if err != nil {
				t.Errorf("fail serve http request %s %v", tt.healthCheckURL, err)
			}
			if res == nil {
				t.Errorf("got nil http response with http request %s", tt.healthCheckURL)
				return
			}
			if res.StatusCode != tt.expectStatusCode {
				t.Errorf("want statusCode %d but got %d", tt.expectStatusCode, res.StatusCode)
			}
			health, err := parseHealthOutput(res.Body)
			if err != nil {
				t.Errorf("fail parse health check output %v", err)
			}
			if health.Health != tt.expectHealth {
				t.Errorf("want health %s but got %s", tt.expectHealth, health.Health)
			}
		})
	}
}

func parseHealthOutput(body io.Reader) (Health, error) {
	obj := Health{}
	d, derr := io.ReadAll(body)
	if derr != nil {
		return obj, derr
	}
	if err := json.Unmarshal(d, &obj); err != nil {
		return obj, err
	}
	return obj, nil
}
