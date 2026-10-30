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
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/models"
	"github.com/wso2/agent-manager/agent-manager-service/repositories/repomocks"
)

// TestBroadcastRotate_PreservesStoredKeyUUID verifies that when a key already exists in
// the store, broadcastRotateToGateways reuses its UUID in the apikey.updated event rather
// than generating a random one. The gateway controller's replica-sync handler matches
// events against its in-memory store by (artifactUUID, keyName, uuid); a random UUID
// on every rotation defeats the stable-identity fix.
func TestBroadcastRotate_PreservesStoredKeyUUID(t *testing.T) {
	const orgID = "org-1"
	const apiID = "my-provider"
	const artifactUUID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const keyName = "my-key"

	storedUUID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	gatewayUUID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")

	hub := &stubEventHub{}

	gatewayRepo := &repomocks.GatewayRepositoryMock{
		GetByOrganizationIDFunc: func(_ context.Context, _ string) ([]*models.Gateway, error) {
			return []*models.Gateway{{UUID: gatewayUUID}}, nil
		},
	}

	upserted := &[]*models.StoredAPIKey{}
	apiKeyRepo := &repomocks.APIKeyRepositoryMock{
		GetByArtifactAndNameFunc: func(_ string, _ string) (*models.StoredAPIKey, error) {
			return &models.StoredAPIKey{
				UUID:    storedUUID,
				Name:    keyName,
				OUID:    orgID,
				Purpose: models.APIKeyPurposeUserManaged,
			}, nil
		},
		UpsertFunc: func(key *models.StoredAPIKey) error {
			*upserted = append(*upserted, key)
			return nil
		},
	}

	b := &apiKeyBroadcaster{
		gatewayRepo:    gatewayRepo,
		gatewayService: NewGatewayEventsService(hub),
		apiKeyRepo:     apiKeyRepo,
	}

	_, err := b.broadcastRotate(context.Background(), orgID, apiID, artifactUUID, keyName,
		&models.RotateAPIKeyRequest{})
	require.NoError(t, err)

	// The upserted key must carry the stored UUID, not a new random one.
	require.Len(t, *upserted, 1)
	assert.Equal(t, storedUUID, (*upserted)[0].UUID,
		"upserted key UUID must match the stored row UUID")

	// The broadcast event's EntityID must also carry the stored UUID.
	require.Len(t, hub.published, 1)
	assert.Equal(t, storedUUID.String(), hub.published[0].EntityID,
		"apikey.updated EntityID must match the stored row UUID, not a random value")
}

// TestBroadcastRotate_FirstRotationGeneratesUUID verifies that when no stored key exists
// (first rotation), broadcastRotateToGateways generates a fresh UUID and uses it for
// both the upsert and the event, so they stay in sync with each other.
func TestBroadcastRotate_FirstRotationGeneratesUUID(t *testing.T) {
	const orgID = "org-1"
	const apiID = "my-provider"
	const artifactUUID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const keyName = "new-key"

	gatewayUUID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	hub := &stubEventHub{}

	gatewayRepo := &repomocks.GatewayRepositoryMock{
		GetByOrganizationIDFunc: func(_ context.Context, _ string) ([]*models.Gateway, error) {
			return []*models.Gateway{{UUID: gatewayUUID}}, nil
		},
	}

	upserted := &[]*models.StoredAPIKey{}
	apiKeyRepo := &repomocks.APIKeyRepositoryMock{
		GetByArtifactAndNameFunc: func(_ string, _ string) (*models.StoredAPIKey, error) {
			return nil, gorm.ErrRecordNotFound
		},
		UpsertFunc: func(key *models.StoredAPIKey) error {
			*upserted = append(*upserted, key)
			return nil
		},
	}

	b := &apiKeyBroadcaster{
		gatewayRepo:    gatewayRepo,
		gatewayService: NewGatewayEventsService(hub),
		apiKeyRepo:     apiKeyRepo,
	}

	_, err := b.broadcastRotate(context.Background(), orgID, apiID, artifactUUID, keyName,
		&models.RotateAPIKeyRequest{})
	require.NoError(t, err)

	require.Len(t, *upserted, 1)
	require.Len(t, hub.published, 1)

	// Upsert UUID and event EntityID must be the same value (even though both are new).
	assert.Equal(t, (*upserted)[0].UUID.String(), hub.published[0].EntityID,
		"upsert UUID and event EntityID must match each other on first rotation")
}
