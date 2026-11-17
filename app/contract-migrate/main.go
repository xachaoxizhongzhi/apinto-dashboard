// contract-migrate performs the one-time inline-IDL migration before upgrading Dashboard.
package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/eolinker/apinto-dashboard/modules/api/contract"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	backup := flag.String("backup", "", "required path for a new gzip JSON backup (mode 0600)")
	flag.Parse()
	dsn := os.Getenv("CONTRACT_MYSQL_DSN")
	if dsn == "" || *backup == "" {
		return fmt.Errorf("set CONTRACT_MYSQL_DSN and -backup; stop Dashboard before migration")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return fmt.Errorf("database connection failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	out, err := os.OpenFile(*backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(out)
	encoder := json.NewEncoder(compressed)
	// Snapshot the affected rows before the first write, including historical versions.
	for _, table := range []string{"version", "history", "publish_history", "api_contract_version", "api_contract_artifact"} {
		if !db.Migrator().HasTable(table) {
			continue
		}
		query := db.Table(table)
		if table == "version" || table == "history" || table == "publish_history" {
			query = query.Where("kind = ?", "api")
		}
		rows, err := query.Rows()
		if err != nil {
			out.Close()
			return err
		}
		for rows.Next() {
			row := map[string]interface{}{}
			if err = db.ScanRows(rows, &row); err != nil {
				rows.Close()
				out.Close()
				return err
			}
			if err = encoder.Encode(map[string]interface{}{"table": table, "row": row}); err != nil {
				rows.Close()
				out.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			out.Close()
			return err
		}
	}
	if err = compressed.Close(); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = contract.MigrateArtifacts(db); err != nil {
		return err
	}
	fmt.Println("Contract artifacts migrated; API IDs, contract version IDs and existing publications unchanged.")
	return nil
}
