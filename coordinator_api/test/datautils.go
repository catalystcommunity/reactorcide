package test

import (
	"crypto/rand"
	"reflect"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// Global counter so data can be unique without users having to track that
var counter int = 0

// DataSetup represents configuration for data setup
type DataSetup map[string]any

// DataUtils contains database connection
type DataUtils struct {
	db *gorm.DB
}

// CreateUser creates a new user with data from DataSetup and random values for missing fields
func (du *DataUtils) CreateUser(setup DataSetup) (*models.User, error) {
	user := &models.User{}

	// Get the type and value of the user struct
	userType := reflect.TypeOf(*user)
	userValue := reflect.ValueOf(user).Elem()

	// Iterate through all fields in the user struct
	for i := 0; i < userType.NumField(); i++ {
		field := userType.Field(i)
		fieldName := field.Name

		// Skip fields that should be handled by the database
		if fieldName == "ID" || fieldName == "CreatedAt" || fieldName == "UpdatedAt" {
			continue
		}

		// Check if the field is in the setup
		if val, ok := setup[fieldName]; ok {
			// Set the field from setup
			fieldValue := userValue.FieldByName(fieldName)
			if fieldValue.CanSet() {
				switch fieldName {
				case "Password", "Salt":
					// Handle byte slices
					switch v := val.(type) {
					case string:
						fieldValue.Set(reflect.ValueOf([]byte(v)))
					case []byte:
						fieldValue.Set(reflect.ValueOf(v))
					default:
						// If it's neither string nor []byte, generate random bytes
						bytes := make([]byte, 16)
						rand.Read(bytes)
						fieldValue.Set(reflect.ValueOf(bytes))
					}
				case "Roles":
					// Handle pq.StringArray
					switch v := val.(type) {
					case []string:
						fieldValue.Set(reflect.ValueOf(pq.StringArray(v)))
					case string:
						fieldValue.Set(reflect.ValueOf(pq.StringArray{v}))
					default:
						// Default to user role
						fieldValue.Set(reflect.ValueOf(pq.StringArray{string(models.UserRoleUser)}))
					}
				default:
					// Handle other types
					rv := reflect.ValueOf(val)
					if rv.Type().AssignableTo(fieldValue.Type()) {
						fieldValue.Set(rv)
					}
				}
			}
		} else {
			// Field not in setup, fill with random data
			fieldValue := userValue.FieldByName(fieldName)
			if fieldValue.CanSet() {
				switch fieldName {
				case "Username":
					fieldValue.SetString(gofakeit.Username())
				case "Email":
					fieldValue.SetString(gofakeit.Email())
				case "Password":
					bytes := make([]byte, 16)
					rand.Read(bytes)
					fieldValue.Set(reflect.ValueOf(bytes))
				case "Salt":
					bytes := make([]byte, 16)
					rand.Read(bytes)
					fieldValue.Set(reflect.ValueOf(bytes))
				case "Roles":
					fieldValue.Set(reflect.ValueOf(pq.StringArray{string(models.UserRoleUser)}))
				}
			}
		}
	}

	// Save the user to the database
	err := du.db.Create(user).Error
	return user, err
}

// CreateJob creates a new job with data from DataSetup and random values for missing fields
// If UserID is not provided in setup, it will create a new user
func (du *DataUtils) CreateJob(setup DataSetup) (*models.Job, error) {
	job := &models.Job{}

	// Check if UserID is provided in setup
	userID, hasUserID := setup["UserID"]
	if !hasUserID || userID == "" {
		// No UserID provided, create a new user
		user, err := du.CreateUser(DataSetup{})
		if err != nil {
			return nil, err
		}

		// Use the new user's ID for the job
		setup["UserID"] = user.UserID
	}

	// Get the type and value of the job struct
	jobType := reflect.TypeOf(*job)
	jobValue := reflect.ValueOf(job).Elem()

	// Iterate through all fields in the job struct
	for i := 0; i < jobType.NumField(); i++ {
		field := jobType.Field(i)
		fieldName := field.Name

		// Skip fields that should be handled by the database or relationships
		if fieldName == "JobID" || fieldName == "CreatedAt" || fieldName == "UpdatedAt" || fieldName == "User" {
			continue
		}

		// Check if the field is in the setup
		if val, ok := setup[fieldName]; ok {
			// Set the field from setup
			fieldValue := jobValue.FieldByName(fieldName)
			if fieldValue.CanSet() {
				// Handle time.Time and *time.Time fields
				if fieldName == "StartedAt" || fieldName == "CompletedAt" {
					switch v := val.(type) {
					case time.Time:
						fieldValue.Set(reflect.ValueOf(&v))
					case string:
						t, err := time.Parse(time.RFC3339, v)
						if err == nil {
							fieldValue.Set(reflect.ValueOf(&t))
						}
					default:
						rv := reflect.ValueOf(val)
						if rv.Type().AssignableTo(fieldValue.Type()) {
							fieldValue.Set(rv)
						}
					}
				} else if fieldName == "JobEnvVars" {
					// Handle JSONB type
					switch v := val.(type) {
					case map[string]string:
						envVars := make(models.JSONB)
						for k, v := range v {
							envVars[k] = v
						}
						fieldValue.Set(reflect.ValueOf(envVars))
					case map[string]interface{}:
						envVars := models.JSONB(v)
						fieldValue.Set(reflect.ValueOf(envVars))
					case models.JSONB:
						fieldValue.Set(reflect.ValueOf(v))
					default:
						rv := reflect.ValueOf(val)
						if rv.Type().AssignableTo(fieldValue.Type()) {
							fieldValue.Set(rv)
						}
					}
				} else {
					// Handle other types
					rv := reflect.ValueOf(val)
					if rv.Type().AssignableTo(fieldValue.Type()) {
						fieldValue.Set(rv)
					}
				}
			}
		} else {
			// Field not in setup, fill with random data
			fieldValue := jobValue.FieldByName(fieldName)
			if fieldValue.CanSet() {
				switch fieldName {
				case "Name":
					fieldValue.SetString("Test Job " + gofakeit.Word())
				case "Description":
					fieldValue.SetString(gofakeit.Sentence(5))
				case "GitURL":
					fieldValue.SetString("https://github.com/" + gofakeit.Username() + "/" + gofakeit.Word() + ".git")
				case "GitRef":
					fieldValue.SetString("main")
				case "SourceType":
					fieldValue.SetString("git")
				case "SourcePath":
					if gofakeit.Bool() {
						fieldValue.SetString("./" + gofakeit.Word())
					}
				case "CodeDir":
					fieldValue.SetString("/job/src")
				case "JobDir":
					fieldValue.SetString("/job/src")
				case "JobCommand":
					fieldValue.SetString("echo 'Hello from test job'")
				case "RunnerImage":
					fieldValue.SetString("quay.io/catalystcommunity/reactorcide_runner")
				case "QueueName":
					fieldValue.SetString("reactorcide-jobs")
				case "AutoTargetState":
					fieldValue.SetString("running")
				case "Status":
					statuses := []string{"submitted", "queued", "running", "completed", "failed"}
					fieldValue.SetString(statuses[gofakeit.Number(0, len(statuses)-1)])
				case "TimeoutSeconds":
					fieldValue.SetInt(int64(gofakeit.Number(300, 3600)))
				case "Priority":
					fieldValue.SetInt(int64(gofakeit.Number(0, 10)))
				case "ExitCode":
					if gofakeit.Bool() {
						exitCode := gofakeit.Number(0, 1)
						fieldValue.Set(reflect.ValueOf(&exitCode))
					}
				}
			}
		}
	}

	// Save the job to the database
	err := du.db.Create(job).Error
	return job, err
}

// CreateAPIToken creates a new API token with data from DataSetup and random values for missing fields
// If UserID is not provided in setup, it will create a new user
func (du *DataUtils) CreateAPIToken(setup DataSetup) (*models.APIToken, error) {
	token := &models.APIToken{}

	// Check if UserID is provided in setup
	userID, hasUserID := setup["UserID"]
	if !hasUserID || userID == "" {
		// No UserID provided, create a new user
		user, err := du.CreateUser(DataSetup{})
		if err != nil {
			return nil, err
		}

		// Use the new user's ID for the token
		setup["UserID"] = user.UserID
	}

	// Get the type and value of the token struct
	tokenType := reflect.TypeOf(*token)
	tokenValue := reflect.ValueOf(token).Elem()

	// Iterate through all fields in the token struct
	for i := 0; i < tokenType.NumField(); i++ {
		field := tokenType.Field(i)
		fieldName := field.Name

		// Skip fields that should be handled by the database or relationships
		if fieldName == "TokenID" || fieldName == "CreatedAt" || fieldName == "UpdatedAt" || fieldName == "User" {
			continue
		}

		// Check if the field is in the setup
		if val, ok := setup[fieldName]; ok {
			// Set the field from setup
			fieldValue := tokenValue.FieldByName(fieldName)
			if fieldValue.CanSet() {
				// Handle byte slices
				if fieldName == "TokenHash" {
					switch v := val.(type) {
					case string:
						fieldValue.Set(reflect.ValueOf([]byte(v)))
					case []byte:
						fieldValue.Set(reflect.ValueOf(v))
					default:
						// Generate random hash if type doesn't match
						bytes := make([]byte, 32)
						rand.Read(bytes)
						fieldValue.Set(reflect.ValueOf(bytes))
					}
				} else if fieldName == "ExpiresAt" || fieldName == "LastUsedAt" {
					// Handle *time.Time fields
					switch v := val.(type) {
					case time.Time:
						fieldValue.Set(reflect.ValueOf(&v))
					case string:
						t, err := time.Parse(time.RFC3339, v)
						if err == nil {
							fieldValue.Set(reflect.ValueOf(&t))
						}
					default:
						rv := reflect.ValueOf(val)
						if rv.Type().AssignableTo(fieldValue.Type()) {
							fieldValue.Set(rv)
						}
					}
				} else if fieldName == "IsActive" {
					// Handle boolean fields explicitly
					switch v := val.(type) {
					case bool:
						fieldValue.SetBool(v)
					default:
						fieldValue.SetBool(true) // Default to true if not a boolean
					}
				} else {
					// Handle other types
					rv := reflect.ValueOf(val)
					if rv.Type().AssignableTo(fieldValue.Type()) {
						fieldValue.Set(rv)
					}
				}
			}
		} else {
			// Field not in setup, fill with random data
			fieldValue := tokenValue.FieldByName(fieldName)
			if fieldValue.CanSet() {
				switch fieldName {
				case "TokenHash":
					bytes := make([]byte, 32)
					rand.Read(bytes)
					fieldValue.Set(reflect.ValueOf(bytes))
				case "Name":
					fieldValue.SetString("Test Token " + gofakeit.Word())
				case "IsActive":
					// Always default to true for new tokens
					fieldValue.SetBool(true)
				case "ExpiresAt":
					// 50% chance of having an expiration date
					if gofakeit.Bool() {
						futureTime := time.Now().UTC().Add(time.Duration(gofakeit.Number(30, 365)) * 24 * time.Hour)
						fieldValue.Set(reflect.ValueOf(&futureTime))
					}
				case "LastUsedAt":
					// 30% chance of having been used
					if gofakeit.Bool() && gofakeit.Bool() && gofakeit.Bool() {
						pastTime := time.Now().UTC().Add(-time.Duration(gofakeit.Number(1, 30)) * 24 * time.Hour)
						fieldValue.Set(reflect.ValueOf(&pastTime))
					}
				}
			}
		}
	}

	// Save the token to the database
	err := du.db.Create(token).Error
	return token, err
}
