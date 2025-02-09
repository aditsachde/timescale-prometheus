package end_to_end_tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/prometheus/common/route"
	"github.com/timescale/timescale-prometheus/pkg/api"
	"github.com/timescale/timescale-prometheus/pkg/internal/testhelpers"
	"github.com/timescale/timescale-prometheus/pkg/pgmodel"
	"github.com/timescale/timescale-prometheus/pkg/query"
)

type labelsResponse struct {
	Status string
	Data   []string
}

func getLabelNamesRequest(apiUrl string) (*http.Request, error) {
	u, err := url.Parse(fmt.Sprintf("%s/labels", apiUrl))

	if err != nil {
		return nil, err
	}

	return http.NewRequest(
		"GET",
		u.String(),
		nil,
	)
}

func getLabelValuesRequest(apiUrl string, labelName string) (*http.Request, error) {
	u, err := url.Parse(fmt.Sprintf("%s/label/%s/values", apiUrl, labelName))

	if err != nil {
		return nil, err
	}

	return http.NewRequest(
		"GET",
		u.String(),
		nil,
	)
}

func TestPromQLLabelEndpoint(t *testing.T) {
	if testing.Short() || !*useDocker {
		t.Skip("skipping integration test")
	}
	withDB(t, *testDatabase, func(db *pgxpool.Pool, t testing.TB) {
		// Ingest test dataset.
		dataset := generateLargeTimeseries()
		if *extendedTest {
			dataset = append(dataset, generateRealTimeseries()...)
		}
		ingestQueryTestDataset(db, t, dataset)
		// Getting a read-only connection to ensure read path is idempotent.
		readOnly := testhelpers.GetReadOnlyConnection(t, *testDatabase)
		defer readOnly.Close()

		var tester *testing.T
		var ok bool
		if tester, ok = t.(*testing.T); !ok {
			t.Fatalf("Cannot run test, not an instance of testing.T")
			return
		}

		r := pgmodel.NewPgxReader(readOnly, nil, 100)
		queryable := query.NewQueryable(r.GetQuerier())

		apiConfig := &api.Config{}
		labelNamesHandler := api.Labels(apiConfig, queryable)
		labelValuesHandler := api.LabelValues(apiConfig, queryable)

		router := route.New()
		router.Get("/api/v1/label/:name/values", labelValuesHandler.ServeHTTP)
		router.Get("/api/v1/labels", labelNamesHandler.ServeHTTP)

		apiURL := fmt.Sprintf("http://%s:%d/api/v1", testhelpers.PromHost, testhelpers.PromPort.Int())
		client := &http.Client{Timeout: 10 * time.Second}

		var (
			requestCases []requestCase
			req          *http.Request
			err          error
		)
		req, err = getLabelNamesRequest(apiURL)
		if err != nil {
			t.Fatalf("unable to create PromQL label names request: %v", err)
		}

		testMethod := testRequest(req, router, client, labelsResultComparator)
		tester.Run("get label names", testMethod)

		labelNames, err := r.GetQuerier().LabelNames()
		if err != nil {
			t.Fatalf("could not get label names from querier")
		}
		labelNames = append(labelNames, "unexisting_label")

		for _, label := range labelNames {
			req, err = getLabelValuesRequest(apiURL, label)
			if err != nil {
				t.Fatalf("unable to create PromQL label values request: %v", err)
			}
			requestCases = append(requestCases, requestCase{req, fmt.Sprintf("get label values for %s", label)})
		}
		testMethod = testRequestConcurrent(requestCases, router, client, labelsResultComparator)
		tester.Run("test label endpoint", testMethod)
	})
}

func labelsResultComparator(promContent []byte, tsContent []byte) error {
	var got, wanted labelsResponse

	err := json.Unmarshal(tsContent, &got)
	if err != nil {
		return fmt.Errorf("unexpected error returned when reading connector response body:\n%s\nbody:\n%s\n", err.Error(), tsContent)
	}

	err = json.Unmarshal(promContent, &wanted)
	if err != nil {
		return fmt.Errorf("unexpected error returned when reading Prometheus response body:\n%s\nbody:\n%s\n", err.Error(), promContent)
	}

	if !reflect.DeepEqual(got, wanted) {
		return fmt.Errorf("unexpected response:\ngot\n%v\nwanted\n%v", got, wanted)
	}

	return nil
}
