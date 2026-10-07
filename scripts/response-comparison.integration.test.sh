#!/bin/sh

set -eu

service=spectre.sample.v1.UserService
ingress_address=$(pwd)/dist/sockets/ingress.sock
egress_candidate_address=$(pwd)/dist/sockets/egress-candidate.sock
escape=$(printf '\033')

# Proctor prefixes each line with a coloured, right-aligned process name and a bar.
process_log() {
	sed "s/${escape}\[[0-9;]*m//g" "$SPECTRE_PROCTOR_LOG" | grep -E "^ *$1 │"
}

count_log() {
	process_log "$1" | grep -Fc "$2" || true
}

wait_for_log() {
	process=$1
	message=$2
	attempt=0
	while [ "$attempt" -lt 100 ]; do
		if process_log "$process" | grep -Fq "$message"; then
			return
		fi
		attempt=$((attempt + 1))
		sleep 0.1
	done
	printf 'timed out waiting for %s to log %s\n' "$process" "$message" >&2
	exit 1
}

fetch() {
	curl --unix-socket "$ingress_address" --fail --silent --show-error --max-time 5 "http://ingress$1"
}

fetch_forecast_as_candidate() {
	curl --unix-socket "$egress_candidate_address" --silent --show-error --max-time 5 \
		--output /dev/null --write-out '%{http_code}' "http://forecasts.example/v1/forecasts/$1"
}

wait_for_log_count() {
	process=$1
	message=$2
	expected=$3
	attempt=0
	while [ "$attempt" -lt 100 ]; do
		count=$(count_log "$process" "$message")
		if [ "$count" -ge "$expected" ]; then
			return
		fi
		attempt=$((attempt + 1))
		sleep 0.1
	done
	printf 'timed out waiting for %s to log %s %s times\n' "$process" "$message" "$expected" >&2
	exit 1
}

verify() {
	equal_response=$(grpcurl -plaintext -unix -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers")
	if ! printf '%s\n' "$equal_response" | grep -Fq 'Alex Example'; then
		printf 'equivalent request did not return the reference response\n' >&2
		exit 1
	fi
	wait_for_log candidate '"path":"/spectre.sample.v1.UserService/ListUsers"'
	wait_for_log ingress '"msg":"Payload normaliser completed","component":"spectre","kind":"field","target":"spectre.sample.v1.User.roles"'
	wait_for_log ingress '"target":"spectre.sample.v1.ListUsersResponse.generatedAt","side":"candidate","payload_path":"$.generatedAt"'
	wait_for_log ingress '"msg":"Response comparison completed","component":"spectre","event":"correlation","method":"POST","path":"/spectre.sample.v1.UserService/ListUsers","outcome":"equivalent"'
	# The comparison timeout has elapsed before this probe, so a second mirror proves
	# the reordered roles did not quarantine the candidate.
	sleep 1.1
	grpcurl -plaintext -unix -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers" >/dev/null
	wait_for_log_count candidate '"path":"/spectre.sample.v1.UserService/ListUsers"' 2

	# Raw HTTP responses are typed by the endpoints that weather.ts declares.
	if ! fetch '/v2/forecast?location=london' | grep -Fq '"alerts"'; then
		printf 'raw HTTP request did not return the reference response\n' >&2
		exit 1
	fi
	wait_for_log ingress '"target":"weather.GetForecastV2Response.alerts"'
	wait_for_log ingress '"msg":"Response comparison completed","component":"spectre","event":"correlation","method":"GET","path":"/v2/forecast","outcome":"equivalent"'
	fetch '/api/v1/forecast?location=sydney' >/dev/null
	wait_for_log ingress '"msg":"Response comparison completed","component":"spectre","event":"correlation","method":"GET","path":"/api/v1/forecast","outcome":"equivalent"'
	fetch /_status >/dev/null
	wait_for_log ingress '"msg":"Response comparison completed","component":"spectre","event":"correlation","method":"GET","path":"/_status","outcome":"equivalent"'

	# Egress replays the reference's forecast to the candidate, so each mirrored
	# request reaches the provider once.
	for side in reference candidate; do
		wait_for_log egress "\"msg\":\"Payload normalisation completed\",\"component\":\"spectre\",\"message\":\"forecasts.FetchForecastRequest\",\"side\":\"$side\",\"normalisers\":0"
	done
	wait_for_log egress '"msg":"Candidate request matched a reference request","component":"spectre","event":"correlation","method":"GET","host":"forecasts.example","path":"/v1/forecasts/london"'
	for location in london sydney; do
		wait_for_log forecasts "\"path\":\"/v1/forecasts/$location\""
		provider_calls=$(count_log forecasts "\"path\":\"/v1/forecasts/$location\"")
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
	wait_for_log candidate '"path":"/spectre.sample.v1.UserService/GetUser"'
	wait_for_log ingress '"msg":"Response comparison completed","component":"spectre","event":"correlation","method":"POST","path":"/spectre.sample.v1.UserService/GetUser","outcome":"divergent"'
	wait_for_log ingress '"msg":"Candidate quarantined"'

	candidate_list_calls=$(count_log candidate '"path":"/spectre.sample.v1.UserService/ListUsers"')
	grpcurl -plaintext -unix -connect-timeout 2 -max-time 5 \
		-d '{"ids":["user-1"]}' "$ingress_address" "$service/ListUsers" >/dev/null
	sleep 0.2
	candidate_list_calls_after=$(count_log candidate '"path":"/spectre.sample.v1.UserService/ListUsers"')
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
	wait_for_log egress '"msg":"Candidate quarantined"'
	status=$(fetch_forecast_as_candidate london)
	if [ "$status" != 503 ]; then
		printf 'quarantined egress returned %s, expected 503\n' "$status" >&2
		exit 1
	fi
	if [ "$(count_log forecasts '"path":"/v1/forecasts/paris"')" -ne 0 ]; then
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
