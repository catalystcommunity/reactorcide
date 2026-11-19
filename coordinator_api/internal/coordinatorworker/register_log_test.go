package coordinatorworker

import (
	"bytes"
	"strings"
	"testing"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/sirupsen/logrus"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

func TestLogRegisteredNamesTheEnrollmentToken(t *testing.T) {
	var buf bytes.Buffer
	old := logging.Log.Out
	oldFormatter := logging.Log.Formatter
	logging.Log.SetOutput(&buf)
	logging.Log.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableTimestamp: true})
	defer func() {
		logging.Log.SetOutput(old)
		logging.Log.SetFormatter(oldFormatter)
	}()

	id, name := "tok-1", "2026 hosts"
	logRegistered(csilapi.RegisterResponse{WorkerId: "w-1", WorkerSession: "SESSION-SECRET", EnrollmentTokenId: &id, EnrollmentTokenName: &name})
	out := buf.String()
	for _, want := range []string{"worker_id=w-1", "enrollment_token_id=tok-1", `enrollment_token_name="2026 hosts"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log %q has no %q", out, want)
		}
	}
	if strings.Contains(out, "SESSION-SECRET") {
		t.Fatal("the register log contains the session token")
	}

	buf.Reset()
	logRegistered(csilapi.RegisterResponse{WorkerId: "w-2"}) // a coordinator before 0.13
	if !strings.Contains(buf.String(), "enrollment_token_id=unknown") {
		t.Fatalf("log %q does not say the token is unknown", buf.String())
	}
}
