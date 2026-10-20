#!/bin/sh

set -eu

service=spectre.sample.v1.UserService
ingress_address=$(pwd)/dist/sockets/ingress.sock
egress_candidate_address=$(pwd)/dist/sockets/egress-candidate.sock
escape=$(printf '\033')

# Proctor prefixes each line with a coloured, right-aligned process name and a bar.
# The proxies and services share the supervisor's output, so their JSON records are
# selected by the spectre.subsystem or revision each one logs. Other lines are skipped.
process_log() {
	# shellcheck disable=SC2016 # $process is a jq variable, bound with --arg.
	case $1 in
	ingress | egress) label=supervisor selector='.["spectre.subsystem"] == $process' ;;
	candidate) label=supervisor selector='.revision == "candidate"' ;;
	*) label=$1 selector=true ;;
	esac
	sed -n -e "s/${escape}\[[0-9;]*m//g" -e "s/^ *$label │ //p" "$SPECTRE_PROCTOR_LOG" |
		jq -cR --arg process "$1" "fromjson? | select($selector)"
}

# count_log counts the records a process logged that match a jq filter.
count_log() {
	process_log "$1" | jq -n "[inputs | select($2)] | length"
}

wait_for_log() {
	wait_for_log_count "$1" "$2" 1
}

wait_for_log_count() {
	process=$1
	filter=$2
	expected=$3
	attempt=0
	while [ "$attempt" -lt 100 ]; do
		count=$(count_log "$process" "$filter")
		if [ "$count" -ge "$expected" ]; then
			return
		fi
		attempt=$((attempt + 1))
		sleep 0.1
	done
	printf 'timed out waiting for %s to log %s %s times\n' "$process" "$filter" "$expected" >&2
	exit 1
}

# comparison matches the record ingress logs when it finishes comparing a route.
comparison() {
	printf '.msg == "Response comparison completed" and .event == "correlation" and .protocol == "http" and .route == "%s" and .outcome == "%s"' "$1" "$2"
}

fetch() {
	curl --unix-socket "$ingress_address" --fail --silent --show-error --max-time 5 "http://ingress$1"
}

fetch_forecast_as_candidate() {
	curl --unix-socket "$egress_candidate_address" --silent --show-error --max-time 5 \
		--output /dev/null --write-out '%{http_code}' "http://forecasts.example/v1/forecasts/$1"
}

verify() {
	list_users='.path == "/spectre.sample.v1.UserService/ListUsers"'
	equal_response=$(grpcurl -plaintext -unix -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers")
	if ! printf '%s\n' "$equal_response" | grep -Fq 'Alex Example'; then
		printf 'equivalent request did not return the reference response\n' >&2
		exit 1
	fi
	wait_for_log candidate "$list_users"
	wait_for_log ingress '.msg == "Payload normaliser completed" and .kind == "field" and .target == "spectre.sample.v1.User.roles"'
	wait_for_log ingress '.target == "spectre.sample.v1.ListUsersResponse.generatedAt" and .side == "candidate" and .payload_path == "$.generatedAt"'
	wait_for_log ingress "$(comparison 'POST /spectre.sample.v1.UserService/ListUsers' equivalent)"
	# The comparison timeout has elapsed before this probe, so a second mirror proves
	# the reordered roles did not quarantine the candidate.
	sleep 1.1
	grpcurl -plaintext -unix -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers" >/dev/null
	wait_for_log_count candidate "$list_users" 2

	# Raw HTTP responses are typed by the endpoints that weather.ts declares.
	if ! fetch '/v2/forecast?location=london' | grep -Fq '"alerts"'; then
		printf 'raw HTTP request did not return the reference response\n' >&2
		exit 1
	fi
	wait_for_log ingress '.target == "weather.GetForecastV2Response.alerts"'
	wait_for_log ingress "$(comparison 'GET /v2/forecast' equivalent)"
	fetch '/api/v1/forecast?location=sydney' >/dev/null
	wait_for_log ingress "$(comparison 'GET /api/v1/forecast' equivalent)"
	fetch /_status >/dev/null
	wait_for_log ingress "$(comparison 'GET /_status' equivalent)"

	# Egress replays the reference's forecast to the candidate, so each mirrored
	# request reaches the provider once.
	for side in reference candidate; do
		wait_for_log egress ".msg == \"Payload normalisation completed\" and .message == \"forecasts.FetchForecastRequest\" and .side == \"$side\" and .normalisers == 0"
	done
	wait_for_log egress '.msg == "Candidate request matched a reference request" and .event == "correlation" and .protocol == "http" and .route == "GET forecasts.example/v1/forecasts/{location}"'
	for location in london sydney; do
		wait_for_log forecasts ".path == \"/v1/forecasts/$location\""
		provider_calls=$(count_log forecasts ".path == \"/v1/forecasts/$location\"")
		if [ "$provider_calls" -ne 1 ]; then
			printf 'forecast provider received %s requests for %s, expected 1\n' "$provider_calls" "$location" >&2
			exit 1
		fi
	done

	different_response=$(grpcurl -plaintext -unix -connect-timeout 2 -max-time 5 \
		-d '{"id":"user-2"}' "$ingress_address" "$service/GetUser")
	if ! printf '%s\n' "$different_response" | grep -Fq 'Sam Sample'; then
		printf 'divergent request did not return the reference response\n' >&2
		exit 1
	fi
	wait_for_log candidate '.path == "/spectre.sample.v1.UserService/GetUser"'
	wait_for_log ingress "$(comparison 'POST /spectre.sample.v1.UserService/GetUser' divergent)"
	wait_for_log ingress '.msg == "Candidate quarantined"'

	candidate_list_calls=$(count_log candidate "$list_users")
	grpcurl -plaintext -unix -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers" >/dev/null
	sleep 0.2
	candidate_list_calls_after=$(count_log candidate "$list_users")
	if [ "$candidate_list_calls_after" -ne "$candidate_list_calls" ]; then
		printf 'candidate received a request after response divergence\n' >&2
		exit 1
	fi

	# A candidate dependency call that the reference never made quarantines egress
	# without reaching the provider.
	status=$(fetch_forecast_as_candidate paris)
	if [ "$status" != 502 ]; then
		printf 'unmatched candidate dependency call returned %s, expected 502\n' "$status" >&2
		exit 1
	fi
	wait_for_log egress '.msg == "Candidate quarantined"'
	status=$(fetch_forecast_as_candidate london)
	if [ "$status" != 503 ]; then
		printf 'quarantined egress returned %s, expected 503\n' "$status" >&2
		exit 1
	fi
	if [ "$(count_log forecasts '.path == "/v1/forecasts/paris"')" -ne 0 ]; then
		printf 'forecast provider received an unmatched candidate request\n' >&2
		exit 1
	fi

	: > "$SPECTRE_PROCTOR_RESULT"
}

if [ "${1:-}" = verify ]; then
	verify
	exit 0
fi

source_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
test_root=$(mktemp -d)
proctor_pid=
# shellcheck disable=SC2329 # Invoked indirectly by trap.
cleanup() {
	if [ -n "$proctor_pid" ] && kill -0 "$proctor_pid" 2>/dev/null; then
		kill -TERM "$proctor_pid" 2>/dev/null || true
		wait "$proctor_pid" 2>/dev/null || true
	fi
	rm -rf "$test_root"
}
trap cleanup EXIT HUP INT TERM

sed -e 's/"ROLE_ADMIN", "ROLE_EDITOR"/"ROLE_EDITOR", "ROLE_ADMIN"/' \
	-e 's/"team": "platform"/"team": "candidate"/' \
	"$source_root/internal/sample/testdata/users.json" > "$test_root/candidate.json"
first_alert='"id": "alert-1", "severity": "moderate", "headline": "Heavy rain"'
last_alert='"id": "alert-3", "severity": "minor", "headline": "Fog"'
sed -e "s/$first_alert/SWAPPED/" -e "s/$last_alert/$first_alert/" -e "s/SWAPPED/$last_alert/" \
	"$source_root/internal/sample/testdata/weather.json" > "$test_root/candidate-weather.json"
export SPECTRE_CANDIDATE_DATA="$test_root/candidate.json"
export SPECTRE_CANDIDATE_WEATHER="$test_root/candidate-weather.json"
export SPECTRE_SCRIPTS_DIR="$source_root/internal/sample/scripts"
export SPECTRE_PROCTOR_LOG="$test_root/proctor.log"
export SPECTRE_PROCTOR_RESULT="$test_root/passed"

cd "$source_root"
proctor "$source_root/Procfile.integration" > "$SPECTRE_PROCTOR_LOG" 2>&1 &
proctor_pid=$!

attempt=0
while [ "$attempt" -lt 600 ]; do
	if [ -f "$SPECTRE_PROCTOR_RESULT" ]; then
		kill -TERM "$proctor_pid" 2>/dev/null || true
		wait "$proctor_pid" 2>/dev/null || true
		proctor_pid=
		printf 'Response comparison integration test passed\n'
		exit 0
	fi
	if ! kill -0 "$proctor_pid" 2>/dev/null; then
		wait "$proctor_pid" 2>/dev/null || true
		proctor_pid=
		cat "$SPECTRE_PROCTOR_LOG" >&2
		exit 1
	fi
	attempt=$((attempt + 1))
	sleep 0.1
done

printf 'response comparison integration test timed out\n' >&2
cat "$SPECTRE_PROCTOR_LOG" >&2
exit 1
