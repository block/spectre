package mysqlserver

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// credentials is the user and password that the proxy connects to the database with.
type credentials struct {
	user     string
	password string
}

// credentialSource returns the proxy's credentials. Stale ones, such as those the
// database refused, are read again.
type credentialSource func(ctx context.Context, stale bool) (credentials, error)

// newCredentialSource reads the credentials from the secret or password file in config.
func newCredentialSource(config Config) credentialSource {
	if config.UpstreamSecret != "" {
		return newSecretCredentials(secretsManagerValue(config.UpstreamSecret)).get
	}
	return passwordFile(config.UpstreamUser, config.UpstreamPasswordFile)
}

// passwordFile reads the password of user from path for every connection, so the file
// can be rotated.
func passwordFile(user, path string) credentialSource {
	return func(_ context.Context, _ bool) (credentials, error) {
		password, err := os.ReadFile(path)
		if err != nil {
			return credentials{}, errors.Wrap(err, "read the MySQL password file")
		}
		return credentials{user: user, password: strings.TrimRight(string(password), "\r\n")}, nil
	}
}

// secretCredentials caches the credentials of a secret until they are stale.
type secretCredentials struct {
	value  func(ctx context.Context) (string, error)
	mu     sync.Mutex
	cached Option[credentials]
}

// newSecretCredentials reads credentials from the JSON secret that value returns.
func newSecretCredentials(value func(ctx context.Context) (string, error)) *secretCredentials {
	return &secretCredentials{value: value}
}

// get returns the cached credentials, or reads the secret when there are none or they
// are stale.
func (s *secretCredentials) get(ctx context.Context, stale bool) (credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.cached.Get(); ok && !stale {
		return cached, nil
	}
	value, err := s.value(ctx)
	if err != nil {
		return credentials{}, err
	}
	var secret struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(value), &secret); err != nil {
		return credentials{}, errors.Wrap(err, "decode the MySQL secret")
	}
	if secret.Username == "" || secret.Password == "" {
		return credentials{}, errors.New("MySQL secret has no username or password")
	}
	read := credentials{user: secret.Username, password: secret.Password}
	s.cached = Some(read)
	return read, nil
}

// secretsManagerValue reads secret from AWS Secrets Manager, with the credentials and
// region that the AWS SDK finds in the environment.
func secretsManagerValue(secret string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		config, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return "", errors.Wrap(err, "load the AWS configuration")
		}
		output, err := secretsmanager.NewFromConfig(config).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
			SecretId: aws.String(secret),
		})
		if err != nil {
			return "", errors.Wrap(err, "read the MySQL secret")
		}
		return aws.ToString(output.SecretString), nil
	}
}
