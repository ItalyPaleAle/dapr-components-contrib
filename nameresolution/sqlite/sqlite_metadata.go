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

package sqlite

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	authSqlite "github.com/dapr/components-contrib/internal/authentication/sqlite"
	"github.com/dapr/components-contrib/metadata"
	"github.com/dapr/components-contrib/nameresolution"
	"github.com/dapr/kit/logger"
)

const (
	defaultTableName         = "hosts"
	defaultMetadataTableName = "metadata"
	defaultUpdateInterval    = 5 * time.Second
	defaultCleanupInternal   = time.Hour
)

type sqliteMetadata struct {
	// Config options - passed by the user via the Configuration resource
	authSqlite.SqliteAuthMetadata `mapstructure:",squash"`

	TableName         string        `mapstructure:"tableName"`
	MetadataTableName string        `mapstructure:"metadataTableName"`
	UpdateInterval    time.Duration `mapstructure:"updateInterval"` // Units smaller than seconds are not accepted
	CleanupInterval   time.Duration `mapstructure:"cleanupInterval" mapstructurealiases:"cleanupIntervalInSeconds"`

	// Instance properties - these are passed by the runtime
	appID       string
	hostAddress string
	port        int
}

func (m *sqliteMetadata) InitWithMetadata(meta nameresolution.Metadata, log logger.Logger) error {
	// Reset the object
	m.reset()

	// Validate the instance properties
	m.appID = meta.GetAppID()
	if m.appID == "" {
		return errors.New("name is missing")
	}
	m.hostAddress = meta.GetHostAddress()
	if m.hostAddress == "" {
		return errors.New("address is missing")
	}
	m.port = meta.GetDaprPort()
	if m.port == 0 {
		return errors.New("port is missing or invalid")
	}

	// Decode the configuration using DecodeMetadata
	err := metadata.DecodeMetadata(meta.Configuration, &m)
	if err != nil {
		return err
	}

	// Validate and sanitize configuration
	err = m.SqliteAuthMetadata.Validate()
	if err != nil {
		return err
	}
	if !authSqlite.ValidIdentifier(m.TableName) {
		return fmt.Errorf("invalid identifier for table name: %s", m.TableName)
	}
	if !authSqlite.ValidIdentifier(m.MetadataTableName) {
		return fmt.Errorf("invalid identifier for metadata table name: %s", m.MetadataTableName)
	}

	// For updateInterval, we do not accept units smaller than seconds due to implementation limitations with SQLite
	if m.UpdateInterval != m.UpdateInterval.Truncate(time.Second) {
		return errors.New("update interval must not contain fractions of seconds")
	}
	// UpdateInterval must also be greater than Timeout
	if (m.UpdateInterval - m.Timeout) < time.Second {
		return errors.New("update interval must be at least 1s greater than timeout")
	}

	// Show a warning if SQLite is configured with an in-memory DB
	if m.SqliteAuthMetadata.IsMemory() {
		log.Warn("Configuring name resolution with an in-memory SQLite database. Service invocation across differet apps will not work.")
	}

	return nil
}

func (m sqliteMetadata) GetAddress() string {
	return net.JoinHostPort(m.hostAddress, strconv.Itoa(m.port))
}

// Reset the object
func (m *sqliteMetadata) reset() {
	m.SqliteAuthMetadata.Reset()

	m.TableName = defaultTableName
	m.MetadataTableName = defaultMetadataTableName
	m.UpdateInterval = defaultCleanupInternal
	m.CleanupInterval = defaultCleanupInternal

	m.appID = ""
	m.hostAddress = ""
	m.port = 0
}
