package source

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/github"
)

// Extrait réduit d'un vrai journal de job (format GitHub Actions).
const sampleJobLog = "\uFEFF" + `2026-11-26T11:14:04.5547077Z Current runner version: '2.338.2'
2026-11-26T11:14:04.5563470Z ##[group]Runner Image Provisioner
2026-11-26T11:14:04.5564045Z Hosted Compute Agent
2026-11-26T11:14:04.5566847Z ##[endgroup]
2026-11-26T11:14:05.1287483Z ##[group]Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
2026-11-26T11:14:05.1287999Z with:
2026-11-26T11:14:05.1288999Z ##[endgroup]
2026-11-26T11:14:05.2000000Z Syncing repository: LeBaptouBaptiste/ContainerMaxxing
2026-11-26T11:14:05.3000000Z [command]/usr/bin/git version
2026-11-26T11:14:06.2727559Z ##[group]Run if [[ "$GITHUB_REF" == refs/tags/v* ]]; then
2026-11-26T11:14:06.2727600Z ` + "\x1b[36;1m" + `version="$GITHUB_REF_NAME"` + "\x1b[0m" + `
2026-11-26T11:14:06.2728000Z ##[endgroup]
2026-11-26T11:14:06.2800000Z version=v1.1.0
2026-11-26T11:14:06.2973678Z ##[group]Run docker build --pull \
2026-11-26T11:14:06.2974000Z ##[endgroup]
2026-11-26T11:14:06.4000000Z #1 [internal] load build definition from Dockerfile
2026-11-26T11:14:39.3000000Z ##[warning]image lourde
2026-11-26T11:14:39.3373786Z ##[group]Run docker run --rm "$TRIVY_IMAGE" image \
2026-11-26T11:14:39.3374000Z ##[endgroup]
2026-11-26T11:14:56.5306734Z │ image.tar (debian 13.8) │  debian  │        0        │
2026-11-26T11:14:56.6027665Z critical=0
2026-11-26T11:14:58.0000000Z ##[error]Process completed with exit code 1.
2026-11-26T11:15:01.7000000Z Post job cleanup.
2026-11-26T11:15:01.7368959Z [command]/usr/bin/git config --local --unset-all
2026-11-26T11:15:01.8017493Z Evaluate and set job outputs
`

func at(s string) *time.Time { t, _ := time.Parse(time.RFC3339, s); return &t }

func sampleSteps() []github.Step {
	done := func(n int, name, start, end, concl string) github.Step {
		return github.Step{Number: n, Name: name, Status: "completed", Conclusion: concl, StartedAt: at(start), CompletedAt: at(end)}
	}
	return []github.Step{
		done(1, "Set up job", "2026-11-26T11:14:04Z", "2026-11-26T11:14:05Z", "success"),
		done(2, "Checkout", "2026-11-26T11:14:05Z", "2026-11-26T11:14:06Z", "success"),
		done(3, "Métadonnées de version", "2026-11-26T11:14:06Z", "2026-11-26T11:14:06Z", "success"),
		done(4, "Build", "2026-11-26T11:14:06Z", "2026-11-26T11:14:39Z", "success"),
		done(5, "Scan Trivy", "2026-11-26T11:14:39Z", "2026-11-26T11:14:58Z", "failure"),
		done(6, "Publier", "2026-11-26T11:14:58Z", "2026-11-26T11:14:58Z", "skipped"),
		done(18, "Post Checkout", "2026-11-26T11:15:01Z", "2026-11-26T11:15:01Z", "success"),
		done(19, "Complete job", "2026-11-26T11:15:01Z", "2026-11-26T11:15:01Z", "success"),
	}
}

func TestSplitJobLog(t *testing.T) {
	got := splitJobLog(sampleJobLog, sampleSteps())
	want := map[int]string{
		2: "Syncing repository: LeBaptouBaptiste/ContainerMaxxing\n$ /usr/bin/git version",
		3: "version=v1.1.0", // même seconde que le step 4 : séparé grâce à l'ordre des groupes
		4: "#1 [internal] load build definition from Dockerfile\nwarning: image lourde",
		5: "│ image.tar (debian 13.8) │  debian  │        0        │\ncritical=0\nerror: Process completed with exit code 1.",
	}
	if len(got) != len(want) {
		t.Fatalf("steps avec extrait = %v, attendu %v", keys(got), keys(want))
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("step %d :\n got %q\nwant %q", n, got[n], w)
		}
	}
}

func TestSplitJobLogTail(t *testing.T) {
	var b strings.Builder
	b.WriteString("2026-11-26T11:00:00.0000000Z ##[group]Run make\n2026-11-26T11:00:00.1000000Z ##[endgroup]\n")
	for i := 0; i < 40; i++ {
		b.WriteString("2026-11-26T11:00:01.0000000Z ligne " + strings.Repeat("x", 300) + "\n")
	}
	got := splitJobLog(b.String(), []github.Step{{Number: 2, Name: "make", Status: "completed", Conclusion: "success",
		StartedAt: at("2026-11-26T11:00:00Z"), CompletedAt: at("2026-11-26T11:00:02Z")}})[2]
	lines := strings.Split(got, "\n")
	if len(lines) != logTailLines {
		t.Fatalf("%d lignes, attendu %d", len(lines), logTailLines)
	}
	if n := len([]rune(lines[0])); n != logLineRunes {
		t.Fatalf("ligne de %d caractères, attendu %d", n, logLineRunes)
	}
}

type logsAPI struct {
	fakeAPI
	jobCalls, runCalls int
	jobErr, runErr     error
	archive            []byte
}

func (l *logsAPI) JobLogs(context.Context, int64) (string, error) {
	l.jobCalls++
	return sampleJobLog, l.jobErr
}

func (l *logsAPI) RunLogs(context.Context, int64) ([]byte, error) {
	l.runCalls++
	return l.archive, l.runErr
}

func TestJobLogCacheDuringRun(t *testing.T) {
	api := &logsAPI{}
	run := &github.Run{ID: 7, Status: "in_progress"}
	jobs := []github.Job{
		{ID: 1, Name: "Build & scan", Status: "completed", Conclusion: "failure", Steps: sampleSteps()},
		{ID: 2, Name: "Deploy", Status: "in_progress"}, // journal pas encore publié : jamais demandé
	}
	var c jobLogCache
	for i := 0; i < 3; i++ {
		got, err := c.logsFor(context.Background(), api, run, jobs)
		if err != nil || got[1][3] != "version=v1.1.0" || got[2] != nil {
			t.Fatalf("poll %d : %v, %v", i, got, err)
		}
	}
	if api.jobCalls != 1 || api.runCalls != 0 {
		t.Fatalf("appels job=%d run=%d, attendu 1 et 0", api.jobCalls, api.runCalls)
	}

	// 404 (journal pas encore publié) : silencieux, redemandé au poll suivant.
	api2 := &logsAPI{jobErr: github.ErrNotFound}
	var c2 jobLogCache
	for i := 0; i < 2; i++ {
		if _, err := c2.logsFor(context.Background(), api2, run, jobs[:1]); err != nil {
			t.Fatalf("404 remonté comme erreur : %v", err)
		}
	}
	if api2.jobCalls != 2 {
		t.Fatalf("%d appels, attendu 2", api2.jobCalls)
	}
}

func TestJobLogCacheRunArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"0_Build & scan.txt":            sampleJobLog,
		"Build & scan/system.txt":       "runner",
		"Build & scan/1_Set up job.txt": "2026-11-26T11:14:04.0000000Z Current runner version",
		"Build & scan/2_Installer cosign.txt": "2026-11-26T11:15:39.0779204Z ##[group]Run sigstore/cosign-installer@881c\n" +
			"2026-11-26T11:15:39.0788038Z ##[endgroup]\n" +
			"2026-11-26T11:15:39.0899527Z ##[group]Run #!/bin/bash\n" +
			"2026-11-26T11:15:39.1115513Z ##[endgroup]\n" +
			"2026-11-26T11:15:39.2000000Z INFO: bootstrap version successfully verified\n",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()

	api := &logsAPI{archive: buf.Bytes()}
	run := &github.Run{ID: 8, Status: "completed"}
	jobs := []github.Job{{ID: 1, Name: "Build & scan", Status: "completed", Conclusion: "success", Steps: sampleSteps()}}
	var c jobLogCache
	for i := 0; i < 2; i++ {
		got, err := c.logsFor(context.Background(), api, run, jobs)
		if err != nil {
			t.Fatal(err)
		}
		if w := "INFO: bootstrap version successfully verified"; got[1][2] != w {
			t.Fatalf("step 2 = %q, attendu %q", got[1][2], w)
		}
		if _, ok := got[1][1]; !ok {
			t.Fatalf("step 1 absent : %v", got[1])
		}
	}
	if api.runCalls != 1 || api.jobCalls != 0 {
		t.Fatalf("appels run=%d job=%d, attendu 1 et 0 (archive exacte, téléchargée une fois)", api.runCalls, api.jobCalls)
	}
}

func keys(m map[int]string) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCleanSegmentSpinner(t *testing.T) {
	seg := parseLogLines("2026-11-26T11:15:55.4Z / Running ..\n2026-11-26T11:15:56.4Z \\ Running ..\n2026-11-26T11:16:18Z Revision  Image")
	if got := cleanSegment(seg); got != "Revision  Image" {
		t.Fatalf("got %q", got)
	}
}
