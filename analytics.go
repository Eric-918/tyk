package main

import (
	"encoding/csv"
	"fmt"
	"github.com/nu7hatch/gouuid"
	"github.com/vmihailenco/msgpack"
	"labix.org/v2/mgo"
	"os"
	"strconv"
	"time"
)

// AnalyticsRecord encodes the details of a request
type AnalyticsRecord struct {
	Method        string
	Path          string
	ContentLength int64
	UserAgent     string
	Day           int
	Month         time.Month
	Year          int
	Hour          int
	ResponseCode  int
	APIKey        string
	TimeStamp     time.Time
	APIVersion    string
	APIName       string
	ApiId         string
	OrgId         string
}

// AnalyticsError is an error for when writing to the storage engine fails
type AnalyticsError struct{}

func (e AnalyticsError) Error() string {
	return "Recording request failed!"
}

// AnalyticsHandler is an interface to record analytics data to a writer.
type AnalyticsHandler interface {
	RecordHit(AnalyticsRecord) error
}

// Purger is an interface that will define how the in-memory store will be purged
// of analytics data to prevent it growing too large
type Purger interface {
	PurgeCache()
	StartPurgeLoop(int)
}

// RedisAnalyticsHandler implements AnalyticsHandler and will record analytics
// data to a redis back end as defined in the Config object
type RedisAnalyticsHandler struct {
	Store *RedisStorageManager
	Clean Purger
}

// RecordHit will store an AnalyticsRecord in Redis
func (r RedisAnalyticsHandler) RecordHit(thisRecord AnalyticsRecord) error {
	encoded, err := msgpack.Marshal(thisRecord)
	u5, _ := uuid.NewV4()

	keyName := fmt.Sprintf("%d%d%d%d-%s", thisRecord.Year, thisRecord.Month, thisRecord.Day, thisRecord.Hour, u5.String())

	if err != nil {
		log.Error("Error encoding analytics data:")
		log.Error(err)
		return AnalyticsError{}
	} else {
		r.Store.SetKey(keyName, string(encoded), 0)
	}

	return nil
}

// CSVPurger purges the in-memory analytics store to a CSV file as defined in the Config object
type CSVPurger struct {
	Store *RedisStorageManager
}

// StartPurgeLoop is used as a goroutine to ensure that the cache is purged
// of analytics data (assuring size is small).
func (c CSVPurger) StartPurgeLoop(nextCount int) {
	time.Sleep(time.Duration(nextCount) * time.Second)
	c.PurgeCache()
	c.StartPurgeLoop(nextCount)
}

// PurgeCache Will pull all the analytics data from the
// cache and drop it to a storage engine, in this case a CSV file
func (c CSVPurger) PurgeCache() {
	curtime := time.Now()
	fname := fmt.Sprintf("%s%d-%s-%d-%d-%d.csv", config.AnalyticsConfig.CSVDir, curtime.Year(), curtime.Month().String(), curtime.Day(), curtime.Hour(), curtime.Minute())

	ferr := os.MkdirAll(config.AnalyticsConfig.CSVDir, 0777)
	if ferr != nil {
		log.Error(ferr)
	}
	outfile, _ := os.Create(fname)
	defer outfile.Close()
	writer := csv.NewWriter(outfile)

	var headers = []string{"METHOD", "PATH", "SIZE", "UA", "DAY", "MONTH", "YEAR", "HOUR", "RESPONSE", "APINAME", "APIVERSION"}

	err := writer.Write(headers)
	if err != nil {
		log.Error("Failed to write file headers!")
		log.Error(err)
	} else {
		KeyValueMap := c.Store.GetKeysAndValues()
		keys := []string{}

		for k, v := range KeyValueMap {
			keys = append(keys, k)
			decoded := AnalyticsRecord{}
			err := msgpack.Unmarshal([]byte(v), &decoded)
			if err != nil {
				log.Error("Couldn't unmarshal analytics data:")
				log.Error(err)
			} else {
				toWrite := []string{
					decoded.Method,
					decoded.Path,
					strconv.FormatInt(decoded.ContentLength, 10),
					decoded.UserAgent,
					strconv.Itoa(decoded.Day),
					decoded.Month.String(),
					strconv.Itoa(decoded.Year),
					strconv.Itoa(decoded.Hour),
					strconv.Itoa(decoded.ResponseCode),
					decoded.APIName,
					decoded.APIVersion}
				err := writer.Write(toWrite)
				if err != nil {
					log.Error("File write failed!")
					log.Error(err)
				}
			}
		}
		writer.Flush()
		c.Store.DeleteKeys(keys)
	}
}

// MongoPurger will purge analytics data into a Mongo database, requires that the Mongo DB string is specified
// in the Config object
type MongoPurger struct {
	Store     *RedisStorageManager
	dbSession *mgo.Session
}

// Connect Connects to Mongo
func (m *MongoPurger) Connect() {
	var err error
	m.dbSession, err = mgo.Dial(config.AnalyticsConfig.MongoURL)
	if err != nil {
		log.Error("Mongo connection failed:")
		log.Panic(err)
	}
}

// StartPurgeLoop starts the loop that will be started as a goroutine and pull data out of the in-memory
// store and into MongoDB
func (m MongoPurger) StartPurgeLoop(nextCount int) {
	time.Sleep(time.Duration(nextCount) * time.Second)
	m.PurgeCache()
	m.StartPurgeLoop(nextCount)
}

// PurgeCache will pull the data from the in-memory store and drop it into the specified MongoDB collection
func (m *MongoPurger) PurgeCache() {
	if m.dbSession == nil {
		log.Info("Not connected to analytics store, connecting...")
		m.Connect()
		m.PurgeCache()
	} else {
		analyticsCollection := m.dbSession.DB("").C(config.AnalyticsConfig.MongoCollection)
		KeyValueMap := m.Store.GetKeysAndValues()

		if len(KeyValueMap) > 0 {
			keys := make([]interface{}, len(KeyValueMap), len(KeyValueMap))
			keyNames := make([]string, len(KeyValueMap), len(KeyValueMap))

			i := 0
			for k, v := range KeyValueMap {
				keyNames[i] = k
				decoded := AnalyticsRecord{}
				err := msgpack.Unmarshal([]byte(v), &decoded)
				if err != nil {
					log.Error("Couldn't unmarshal analytics data:")
					log.Error(err)
				} else {
					keys[i] = interface{}(decoded)
				}
				i += 1
			}

			err := analyticsCollection.Insert(keys...)
			if err != nil {
				log.Error("Problem inserting to mongo collection")
				log.Error(err)
			} else {
				m.Store.DeleteKeys(keyNames)
			}
		}
	}

}
