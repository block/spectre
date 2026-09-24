reference **/*.go !**/*_test.go ready=http:50051/readyz=204: spectre-sample
candidate **/*.go !**/*_test.go ready=http:50052/readyz=204: spectre-sample --listen=127.0.0.1:50052
ingress after=reference,candidate ready=http:50050/readyz=204: spectre-ingress --reference=h2c://127.0.0.1:50051 --candidate=h2c://127.0.0.1:50052
