setup! : mkdir -p dist/sockets
reference **/*.go !**/*_test.go after=setup ready=http+unix:dist/sockets/reference.sock:/readyz=204: spectre-sample --listen="unix:$(pwd)/dist/sockets/reference.sock"
candidate **/*.go !**/*_test.go after=setup ready=http+unix:dist/sockets/candidate.sock:/readyz=204: spectre-sample --listen="unix:$(pwd)/dist/sockets/candidate.sock"
ingress **/*.go !**/*_test.go internal/sample/scripts/**/*.js dist/sample.pb after=reference,candidate ready=http+unix:dist/sockets/ingress.sock:/readyz=204: spectre-ingress --listen="unix:$(pwd)/dist/sockets/ingress.sock" --reference="h2c+unix:$(pwd)/dist/sockets/reference.sock" --candidate="h2c+unix:$(pwd)/dist/sockets/candidate.sock" --scripts-dir=internal/sample/scripts --descriptor-set=dist/sample.pb --log-level=debug
