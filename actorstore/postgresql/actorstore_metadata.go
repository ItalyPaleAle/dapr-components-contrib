/*
Copyright 2023 The Dapr Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package postgresql

import (
	"errors"
	"time"

	"github.com/dapr/components-contrib/actorstore"
	pgauth "github.com/dapr/components-contrib/internal/authentication/postgresql"
	"github.com/dapr/components-contrib/metadata"
)

type pgTable string

const (
	pgTableHosts           pgTable = "hosts"
	pgTableHostsActorTypes pgTable = "hosts_actor_types"
	pgTableActors          pgTable = "actors"
)

type pgMetadata struct {
	pgauth.PostgresAuthMetadata `mapstructure:",squash"`

	TablePrefix       string        `mapstructure:"tablePrefix"`       // Could be in the format "schema.prefix" or just "prefix". Default: empty
	MetadataTableName string        `mapstructure:"metadataTableName"` // Could be in the format "schema.table" or just "table". Default: "dapr_metadata" (same as state store)
	Timeout           time.Duration `mapstructure:"timeout"`           // Default: 20s
}

func (m *pgMetadata) InitWithMetadata(meta actorstore.Metadata) error {
	// Reset the object
	m.PostgresAuthMetadata.Reset()
	m.TablePrefix = ""
	m.MetadataTableName = "dapr_metadata"
	m.Timeout = 20 * time.Second

	// Decode the metadata
	err := metadata.DecodeMetadata(meta.Properties, &m)
	if err != nil {
		return err
	}

	// Validate and sanitize input
	err = m.PostgresAuthMetadata.InitWithMetadata(meta.Properties, true)
	if err != nil {
		return err
	}

	// Timeout
	if m.Timeout < 1*time.Second {
		return errors.New("invalid value for 'timeout': must be greater than 0")
	}

	return nil
}

func (m pgMetadata) TableName(table pgTable) string {
	return m.TablePrefix + string(table)
}