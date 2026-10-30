// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package services

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
	"github.com/wso2/agent-manager/agent-manager-service/utils"
)

type activeDeploymentLookup func(s *GatewayInternalAPIService, ctx context.Context, id, ouID, gatewayID string) (map[string]string, error)

// Both artifact kinds share one lookup; each keeps its own not-found sentinel.
var activeDeploymentLookups = []struct {
	name        string
	lookup      activeDeploymentLookup
	notFound    error
	notNotFound error
}{
	{
		name:        "agent",
		lookup:      (*GatewayInternalAPIService).GetActiveAgentDeploymentByGateway,
		notFound:    utils.ErrAgentArtifactNotFound,
		notNotFound: utils.ErrMCPProxyNotFound,
	},
	{
		name:        "mcp proxy",
		lookup:      (*GatewayInternalAPIService).GetActiveMCPProxyDeploymentByGateway,
		notFound:    utils.ErrMCPProxyNotFound,
		notNotFound: utils.ErrAgentArtifactNotFound,
	},
}

func newGatewayInternalServiceWithDeployments(repo *repomocks.DeploymentRepositoryMock) *GatewayInternalAPIService {
	return NewGatewayInternalAPIService(nil, nil, repo, nil, nil, nil)
}

func TestActiveArtifactDeploymentByGatewayReturnsYamlKeyedByID(t *testing.T) {
	for _, tt := range activeDeploymentLookups {
		t.Run(tt.name, func(t *testing.T) {
			repo := &repomocks.DeploymentRepositoryMock{
				GetCurrentByGatewayFunc: func(artifactUUID, gatewayID, orgUUID string) (*models.Deployment, error) {
					assert.Equal(t, "artifact-1", artifactUUID)
					assert.Equal(t, "gw-1", gatewayID)
					assert.Equal(t, "ou-1", orgUUID)
					return &models.Deployment{Content: []byte("kind: Agent\n")}, nil
				},
			}
			got, err := tt.lookup(newGatewayInternalServiceWithDeployments(repo), context.Background(), "artifact-1", "ou-1", "gw-1")
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"artifact-1": "kind: Agent\n"}, got)
		})
	}
}

func TestActiveArtifactDeploymentByGatewayNotDeployed(t *testing.T) {
	for _, tt := range activeDeploymentLookups {
		t.Run(tt.name, func(t *testing.T) {
			repo := &repomocks.DeploymentRepositoryMock{
				GetCurrentByGatewayFunc: func(string, string, string) (*models.Deployment, error) {
					return nil, nil //nolint:nilnil // the repository reports "no current deployment" this way
				},
			}
			_, err := tt.lookup(newGatewayInternalServiceWithDeployments(repo), context.Background(), "artifact-1", "ou-1", "gw-1")
			assert.ErrorIs(t, err, tt.notFound)
			assert.NotErrorIs(t, err, tt.notNotFound)
			// The gateway controllers rely on this: they map only the kind's own sentinel to 404.
			assert.NotErrorIs(t, err, utils.ErrDeploymentNotActive)
		})
	}
}

func TestActiveArtifactDeploymentByGatewayRepoErrorIsNotNotFound(t *testing.T) {
	repoErr := errors.New("db down")
	for _, tt := range activeDeploymentLookups {
		t.Run(tt.name, func(t *testing.T) {
			repo := &repomocks.DeploymentRepositoryMock{
				GetCurrentByGatewayFunc: func(string, string, string) (*models.Deployment, error) {
					return nil, repoErr
				},
			}
			_, err := tt.lookup(newGatewayInternalServiceWithDeployments(repo), context.Background(), "artifact-1", "ou-1", "gw-1")
			assert.ErrorIs(t, err, repoErr)
			assert.NotErrorIs(t, err, tt.notFound)
		})
	}
}

// The stored Agent YAML holds the proxy's upstream secret only as secretRef; the gateway is served the value.
func TestActiveAgentDeploymentDecryptsTheUpstreamSecret(t *testing.T) {
	key := make([]byte, 32)
	ciphertext, err := utils.EncryptBytes([]byte("s3cret"), key)
	require.NoError(t, err)
	in := proxiedA2AInput()
	secretRef := base64.StdEncoding.EncodeToString(ciphertext)
	in.UpstreamAuth.SecretRef = &secretRef
	stored, err := generateA2AAgentDeploymentYAML(in)
	require.NoError(t, err)
	repo := &repomocks.DeploymentRepositoryMock{
		GetCurrentByGatewayFunc: func(string, string, string) (*models.Deployment, error) {
			return &models.Deployment{Content: []byte(stored)}, nil
		},
	}
	svc := NewGatewayInternalAPIService(nil, nil, repo, nil, nil, key)

	got, err := svc.GetActiveAgentDeploymentByGateway(context.Background(), "artifact-1", "ou-1", "gw-1")
	require.NoError(t, err)

	var doc struct {
		Spec struct {
			Upstream struct {
				Auth map[string]interface{} `yaml:"auth"`
			} `yaml:"upstream"`
		} `yaml:"spec"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(got["artifact-1"]), &doc))
	assert.Equal(t, "s3cret", doc.Spec.Upstream.Auth["value"])
	assert.Equal(t, "X-Upstream-Key", doc.Spec.Upstream.Auth["header"])
	assert.NotContains(t, doc.Spec.Upstream.Auth, "secretRef")
}

func TestActiveArtifactDeploymentByGatewayUnparseableYaml(t *testing.T) {
	for _, tt := range activeDeploymentLookups {
		t.Run(tt.name, func(t *testing.T) {
			repo := &repomocks.DeploymentRepositoryMock{
				GetCurrentByGatewayFunc: func(string, string, string) (*models.Deployment, error) {
					return &models.Deployment{Content: []byte("key: [unclosed")}, nil
				},
			}
			_, err := tt.lookup(newGatewayInternalServiceWithDeployments(repo), context.Background(), "artifact-1", "ou-1", "gw-1")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "failed to resolve secrets")
			assert.NotErrorIs(t, err, tt.notFound)
		})
	}
}

// TestGetActiveLLMProviderDeploymentByGateway_ResolvesProviderByUUIDOrHandle verifies that
// the gateway-controller caller (which always sends a UUID) and the management-API caller
// (which may send a handle) both resolve correctly.
func TestGetActiveLLMProviderDeploymentByGateway_ResolvesProviderByUUIDOrHandle(t *testing.T) {
	providerUUID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	provider := &models.LLMProvider{UUID: providerUUID}

	// resolveSecretsInYAML requires a "spec" key; no secretRef means the YAML
	// is returned unchanged.
	const yamlContent = "kind: LLMProvider\nspec:\n  upstream: {}\n"
	deploymentRepo := &repomocks.DeploymentRepositoryMock{
		GetCurrentByGatewayFunc: func(string, string, string) (*models.Deployment, error) {
			return &models.Deployment{Content: []byte(yamlContent)}, nil
		},
	}

	t.Run("UUID providerID routes to GetByUUID, not GetByHandle", func(t *testing.T) {
		var uuidCalled, handleCalled bool
		providerRepo := &repomocks.LLMProviderRepositoryMock{
			GetByUUIDFunc: func(id, _ string) (*models.LLMProvider, error) {
				uuidCalled = true
				assert.Equal(t, providerUUID.String(), id)
				return provider, nil
			},
			GetByHandleFunc: func(_, _ string) (*models.LLMProvider, error) {
				handleCalled = true
				return nil, nil
			},
		}
		svc := NewGatewayInternalAPIService(providerRepo, nil, deploymentRepo, nil, nil, nil)
		got, err := svc.GetActiveLLMProviderDeploymentByGateway(context.Background(), providerUUID.String(), "ou-1", "gw-1")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{providerUUID.String(): yamlContent}, got)
		assert.True(t, uuidCalled, "GetByUUID should have been called")
		assert.False(t, handleCalled, "GetByHandle must not be called when the ID is a UUID")
	})

	t.Run("handle providerID routes to GetByHandle, not GetByUUID", func(t *testing.T) {
		var uuidCalled, handleCalled bool
		providerRepo := &repomocks.LLMProviderRepositoryMock{
			GetByUUIDFunc: func(_, _ string) (*models.LLMProvider, error) {
				uuidCalled = true
				return nil, nil
			},
			GetByHandleFunc: func(handle, _ string) (*models.LLMProvider, error) {
				handleCalled = true
				assert.Equal(t, "bedrock-claude", handle)
				return provider, nil
			},
		}
		svc := NewGatewayInternalAPIService(providerRepo, nil, deploymentRepo, nil, nil, nil)
		got, err := svc.GetActiveLLMProviderDeploymentByGateway(context.Background(), "bedrock-claude", "ou-1", "gw-1")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"bedrock-claude": yamlContent}, got)
		assert.False(t, uuidCalled, "GetByUUID must not be called when the ID is a handle")
		assert.True(t, handleCalled, "GetByHandle should have been called")
	})

	t.Run("provider not found returns ErrLLMProviderNotFound", func(t *testing.T) {
		providerRepo := &repomocks.LLMProviderRepositoryMock{
			GetByUUIDFunc: func(_, _ string) (*models.LLMProvider, error) {
				return nil, gorm.ErrRecordNotFound
			},
		}
		svc := NewGatewayInternalAPIService(providerRepo, nil, deploymentRepo, nil, nil, nil)
		_, err := svc.GetActiveLLMProviderDeploymentByGateway(context.Background(), providerUUID.String(), "ou-1", "gw-1")
		assert.ErrorIs(t, err, utils.ErrLLMProviderNotFound)
	})
}
