package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mssql"
)

const (
	databaseName = "NorthwindRelay"
	password     = "SupplyChain!2026"
	defaultImage = "mcr.microsoft.com/mssql/server:2022-CU18-ubuntu-22.04"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	image := strings.TrimSpace(os.Getenv("MSSQL_DEMO_IMAGE"))
	if image == "" {
		image = defaultImage
	}

	container, err := mssql.Run(ctx,
		image,
		mssql.WithAcceptEULA(),
		mssql.WithPassword(password),
	)
	if err != nil {
		log.Printf("start SQL Server container: %v", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			log.Printf("terminate SQL Server container: %v", err)
		}
	}()

	adminConn, err := container.ConnectionString(ctx, "encrypt=true", "TrustServerCertificate=true")
	if err != nil {
		log.Printf("build connection string: %v", err)
		return 1
	}

	adminDB, err := sql.Open("sqlserver", adminConn)
	if err != nil {
		log.Printf("open SQL Server connection: %v", err)
		return 1
	}
	defer func() {
		if err := adminDB.Close(); err != nil {
			log.Printf("close SQL Server connection: %v", err)
		}
	}()

	if err := ensureDatabase(ctx, adminDB); err != nil {
		log.Printf("ensure database: %v", err)
		return 1
	}

	conn, err := container.ConnectionString(ctx, "encrypt=true", "TrustServerCertificate=true", "database="+databaseName)
	if err != nil {
		log.Printf("build database connection string: %v", err)
		return 1
	}

	db, err := sql.Open("sqlserver", conn)
	if err != nil {
		log.Printf("open demo database connection: %v", err)
		return 1
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("close demo database connection: %v", err)
		}
	}()

	if err := execBatches(ctx, db, seedSQL); err != nil {
		log.Printf("seed database: %v", err)
		return 1
	}
	if err := verifySeeded(ctx, db); err != nil {
		log.Printf("verify seeded database: %v", err)
		return 1
	}

	u, err := url.Parse(adminConn)
	if err != nil {
		log.Printf("parse connection string: %v", err)
		return 1
	}
	host := u.Hostname()
	port := u.Port()

	if err := writeDemoEnv(host, port); err != nil {
		log.Printf("update .env with demo connection: %v", err)
		return 1
	}

	printInstructions(host, port)

	fmt.Println()
	fmt.Println("SQL Server is ready. Press Enter to stop the demo and remove the container.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	return 0
}

func ensureDatabase(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
IF DB_ID(N'NorthwindRelay') IS NULL
BEGIN
    CREATE DATABASE NorthwindRelay;
END`)
	return err
}

func execBatches(ctx context.Context, db *sql.DB, script string) error {
	for batch := range strings.SplitSeq(script, "\nGO\n") {
		batch = strings.TrimSpace(batch)
		if batch == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, batch); err != nil {
			return fmt.Errorf("execute batch %q: %w", firstLine(batch), err)
		}
	}
	return nil
}

func verifySeeded(ctx context.Context, db *sql.DB) error {
	var tableCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM sys.tables AS t
JOIN sys.schemas AS s ON s.schema_id = t.schema_id
WHERE s.name IN (N'ops', N'finance');`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		return fmt.Errorf("%s has zero user tables after seeding", databaseName)
	}
	fmt.Printf("Seeded %s with %d user tables.\n", databaseName, tableCount)
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 80 {
		return line[:80] + "..."
	}
	return line
}

func writeDemoEnv(host, port string) error {
	replacements := map[string]string{
		"MSSQL_SERVER":                   host,
		"MSSQL_PORT":                     port,
		"MSSQL_DATABASE":                 databaseName,
		"MSSQL_USERNAME":                 "sa",
		"MSSQL_PASSWORD":                 password,
		"MSSQL_TRUST_SERVER_CERTIFICATE": "true",
		"MSSQL_ACCESS_LEVEL":             "READONLY",
	}
	defaults := map[string]string{
		"MSSQL_MCP_SERVER_DIR": "../..",
	}
	order := []string{
		"MSSQL_SERVER",
		"MSSQL_PORT",
		"MSSQL_DATABASE",
		"MSSQL_USERNAME",
		"MSSQL_PASSWORD",
		"MSSQL_TRUST_SERVER_CERTIFICATE",
		"MSSQL_ACCESS_LEVEL",
		"MSSQL_MCP_SERVER_DIR",
	}

	data, err := os.ReadFile(".env")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	seen := make(map[string]bool)
	lines := make([]string, 0)
	if len(data) > 0 {
		for line := range strings.SplitSeq(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			key := envLineKey(line)
			if value, ok := replacements[key]; ok {
				lines = append(lines, key+"="+value)
				seen[key] = true
				continue
			}
			if _, ok := defaults[key]; ok {
				seen[key] = true
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
	}

	missing := make([]string, 0)
	for _, key := range order {
		if seen[key] {
			continue
		}
		if value, ok := replacements[key]; ok {
			missing = append(missing, key+"="+value)
			continue
		}
		if value, ok := defaults[key]; ok {
			missing = append(missing, key+"="+value)
		}
	}
	if len(missing) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "# Updated by supply-chain demo starter.")
		lines = append(lines, missing...)
	}

	return os.WriteFile(".env", []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func envLineKey(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}

func printInstructions(host, port string) {
	fmt.Println("MSSQL demo database is running.")
	fmt.Println("Updated .env with the current container connection.")
	fmt.Println()
	fmt.Println("Environment for mssql-mcp:")
	fmt.Printf("MSSQL_SERVER=%s\n", host)
	fmt.Printf("MSSQL_PORT=%s\n", port)
	fmt.Printf("MSSQL_DATABASE=%s\n", databaseName)
	fmt.Println("MSSQL_USERNAME=sa")
	fmt.Printf("MSSQL_PASSWORD=%s\n", password)
	fmt.Println("MSSQL_TRUST_SERVER_CERTIFICATE=true")
	fmt.Println("MSSQL_ACCESS_LEVEL=READONLY")
	fmt.Println()
	fmt.Println("Example MCP server config:")
	fmt.Println(`{
  "mcpServers": {
    "mssql-northwind-relay": {
      "command": "go",
      "args": ["run", "./cmd/mssql-mcp"],
      "env": {
        "MSSQL_SERVER": "` + host + `",
        "MSSQL_PORT": "` + port + `",
        "MSSQL_DATABASE": "` + databaseName + `",
        "MSSQL_USERNAME": "sa",
        "MSSQL_PASSWORD": "` + password + `",
        "MSSQL_TRUST_SERVER_CERTIFICATE": "true",
        "MSSQL_ACCESS_LEVEL": "READONLY"
      }
    }
  }
}`)
	fmt.Println()
	fmt.Println("Agent prompt:")
	fmt.Println(`You are an operations detective for Northwind Relay. Use the mssql-northwind-relay MCP server to inspect the schema and data. Build a concise findings report that identifies the most important operational, financial, and data-quality risks. Include the SQL evidence behind each finding and recommend the next three actions.`)
}

const seedSQL = `
IF SCHEMA_ID(N'ops') IS NULL EXEC(N'CREATE SCHEMA ops');
GO
IF SCHEMA_ID(N'finance') IS NULL EXEC(N'CREATE SCHEMA finance');
GO
IF OBJECT_ID(N'finance.Payments', N'U') IS NOT NULL DROP TABLE finance.Payments;
IF OBJECT_ID(N'ops.Shipments', N'U') IS NOT NULL DROP TABLE ops.Shipments;
IF OBJECT_ID(N'ops.SalesOrderLines', N'U') IS NOT NULL DROP TABLE ops.SalesOrderLines;
IF OBJECT_ID(N'ops.SalesOrders', N'U') IS NOT NULL DROP TABLE ops.SalesOrders;
IF OBJECT_ID(N'ops.QualityIncidents', N'U') IS NOT NULL DROP TABLE ops.QualityIncidents;
IF OBJECT_ID(N'ops.PurchaseOrderLines', N'U') IS NOT NULL DROP TABLE ops.PurchaseOrderLines;
IF OBJECT_ID(N'ops.PurchaseOrders', N'U') IS NOT NULL DROP TABLE ops.PurchaseOrders;
IF OBJECT_ID(N'ops.InventorySnapshots', N'U') IS NOT NULL DROP TABLE ops.InventorySnapshots;
IF OBJECT_ID(N'ops.Products', N'U') IS NOT NULL DROP TABLE ops.Products;
IF OBJECT_ID(N'ops.Warehouses', N'U') IS NOT NULL DROP TABLE ops.Warehouses;
IF OBJECT_ID(N'ops.Customers', N'U') IS NOT NULL DROP TABLE ops.Customers;
IF OBJECT_ID(N'ops.Vendors', N'U') IS NOT NULL DROP TABLE ops.Vendors;
GO
CREATE TABLE ops.Vendors (
    VendorID INT IDENTITY(1,1) PRIMARY KEY,
    VendorName NVARCHAR(120) NOT NULL,
    CountryCode CHAR(2) NOT NULL,
    RiskTier NVARCHAR(20) NOT NULL,
    OnTimeSLA DECIMAL(5,2) NOT NULL,
    PaymentTermsDays INT NOT NULL
);

CREATE TABLE ops.Warehouses (
    WarehouseID INT IDENTITY(1,1) PRIMARY KEY,
    WarehouseCode NVARCHAR(12) NOT NULL UNIQUE,
    Region NVARCHAR(40) NOT NULL,
    CapacityUnits INT NOT NULL
);

CREATE TABLE ops.Products (
    ProductID INT IDENTITY(1,1) PRIMARY KEY,
    SKU NVARCHAR(40) NOT NULL UNIQUE,
    ProductName NVARCHAR(140) NOT NULL,
    Category NVARCHAR(60) NOT NULL,
    StandardCost DECIMAL(12,2) NOT NULL,
    ListPrice DECIMAL(12,2) NOT NULL,
    ReorderPoint INT NOT NULL,
    PreferredVendorID INT NOT NULL,
    CONSTRAINT FK_Products_PreferredVendor FOREIGN KEY (PreferredVendorID) REFERENCES ops.Vendors(VendorID)
);

CREATE TABLE ops.InventorySnapshots (
    SnapshotID INT IDENTITY(1,1) PRIMARY KEY,
    ProductID INT NOT NULL,
    WarehouseID INT NOT NULL,
    SnapshotDate DATE NOT NULL,
    OnHandUnits INT NOT NULL,
    ReservedUnits INT NOT NULL,
    DamagedUnits INT NOT NULL,
    CONSTRAINT FK_Inventory_Product FOREIGN KEY (ProductID) REFERENCES ops.Products(ProductID),
    CONSTRAINT FK_Inventory_Warehouse FOREIGN KEY (WarehouseID) REFERENCES ops.Warehouses(WarehouseID)
);

CREATE TABLE ops.PurchaseOrders (
    PurchaseOrderID INT IDENTITY(1,1) PRIMARY KEY,
    VendorID INT NOT NULL,
    OrderDate DATE NOT NULL,
    ExpectedDate DATE NOT NULL,
    ActualReceiptDate DATE NULL,
    Status NVARCHAR(20) NOT NULL,
    CONSTRAINT FK_PurchaseOrders_Vendor FOREIGN KEY (VendorID) REFERENCES ops.Vendors(VendorID)
);

CREATE TABLE ops.PurchaseOrderLines (
    PurchaseOrderLineID INT IDENTITY(1,1) PRIMARY KEY,
    PurchaseOrderID INT NOT NULL,
    ProductID INT NOT NULL,
    OrderedUnits INT NOT NULL,
    ReceivedUnits INT NOT NULL,
    UnitCost DECIMAL(12,2) NOT NULL,
    CONSTRAINT FK_POLines_PO FOREIGN KEY (PurchaseOrderID) REFERENCES ops.PurchaseOrders(PurchaseOrderID),
    CONSTRAINT FK_POLines_Product FOREIGN KEY (ProductID) REFERENCES ops.Products(ProductID)
);

CREATE TABLE ops.QualityIncidents (
    IncidentID INT IDENTITY(1,1) PRIMARY KEY,
    ProductID INT NOT NULL,
    VendorID INT NOT NULL,
    WarehouseID INT NOT NULL,
    IncidentDate DATE NOT NULL,
    Severity NVARCHAR(20) NOT NULL,
    DefectCode NVARCHAR(40) NOT NULL,
    AffectedUnits INT NOT NULL,
    RootCause NVARCHAR(200) NULL,
    CONSTRAINT FK_Quality_Product FOREIGN KEY (ProductID) REFERENCES ops.Products(ProductID),
    CONSTRAINT FK_Quality_Vendor FOREIGN KEY (VendorID) REFERENCES ops.Vendors(VendorID),
    CONSTRAINT FK_Quality_Warehouse FOREIGN KEY (WarehouseID) REFERENCES ops.Warehouses(WarehouseID)
);

CREATE TABLE ops.Customers (
    CustomerID INT IDENTITY(1,1) PRIMARY KEY,
    CustomerName NVARCHAR(140) NOT NULL,
    Segment NVARCHAR(40) NOT NULL,
    Region NVARCHAR(40) NOT NULL,
    CreditLimit DECIMAL(12,2) NOT NULL,
    RelatedVendorID INT NULL,
    CONSTRAINT FK_Customers_RelatedVendor FOREIGN KEY (RelatedVendorID) REFERENCES ops.Vendors(VendorID)
);

CREATE TABLE ops.SalesOrders (
    SalesOrderID INT IDENTITY(1,1) PRIMARY KEY,
    CustomerID INT NOT NULL,
    OrderDate DATE NOT NULL,
    RequestedShipDate DATE NOT NULL,
    ActualShipDate DATE NULL,
    Status NVARCHAR(20) NOT NULL,
    CONSTRAINT FK_SalesOrders_Customer FOREIGN KEY (CustomerID) REFERENCES ops.Customers(CustomerID)
);

CREATE TABLE ops.Shipments (
    ShipmentID INT IDENTITY(1,1) PRIMARY KEY,
    SalesOrderID INT NOT NULL,
    WarehouseID INT NOT NULL,
    Carrier NVARCHAR(80) NOT NULL,
    DepartedAt DATETIME2 NULL,
    DeliveredAt DATETIME2 NULL,
    TemperatureExcursionMinutes INT NOT NULL,
    FreightCost DECIMAL(12,2) NOT NULL,
    Status NVARCHAR(20) NOT NULL,
    CONSTRAINT FK_Shipments_SalesOrder FOREIGN KEY (SalesOrderID) REFERENCES ops.SalesOrders(SalesOrderID),
    CONSTRAINT FK_Shipments_Warehouse FOREIGN KEY (WarehouseID) REFERENCES ops.Warehouses(WarehouseID)
);

CREATE TABLE ops.SalesOrderLines (
    SalesOrderLineID INT IDENTITY(1,1) PRIMARY KEY,
    SalesOrderID INT NOT NULL,
    ProductID INT NOT NULL,
    OrderedUnits INT NOT NULL,
    UnitPrice DECIMAL(12,2) NOT NULL,
    DiscountPct DECIMAL(5,2) NOT NULL,
    CONSTRAINT FK_SOLines_SalesOrder FOREIGN KEY (SalesOrderID) REFERENCES ops.SalesOrders(SalesOrderID),
    CONSTRAINT FK_SOLines_Product FOREIGN KEY (ProductID) REFERENCES ops.Products(ProductID)
);

CREATE TABLE finance.Payments (
    PaymentID INT IDENTITY(1,1) PRIMARY KEY,
    CustomerID INT NOT NULL,
    SalesOrderID INT NOT NULL,
    PaymentDate DATE NOT NULL,
    Amount DECIMAL(12,2) NOT NULL,
    Method NVARCHAR(30) NOT NULL,
    Status NVARCHAR(20) NOT NULL,
    CONSTRAINT FK_Payments_Customer FOREIGN KEY (CustomerID) REFERENCES ops.Customers(CustomerID),
    CONSTRAINT FK_Payments_SalesOrder FOREIGN KEY (SalesOrderID) REFERENCES ops.SalesOrders(SalesOrderID)
);
GO
CREATE INDEX IX_Inventory_ProductWarehouseDate ON ops.InventorySnapshots(ProductID, WarehouseID, SnapshotDate);
CREATE INDEX IX_PurchaseOrders_VendorDates ON ops.PurchaseOrders(VendorID, ExpectedDate, ActualReceiptDate);
CREATE INDEX IX_Quality_VendorProductDate ON ops.QualityIncidents(VendorID, ProductID, IncidentDate);
CREATE INDEX IX_SalesOrders_CustomerDates ON ops.SalesOrders(CustomerID, RequestedShipDate, ActualShipDate);
CREATE INDEX IX_Shipments_OrderWarehouse ON ops.Shipments(SalesOrderID, WarehouseID, DeliveredAt);
GO
INSERT INTO ops.Vendors (VendorName, CountryCode, RiskTier, OnTimeSLA, PaymentTermsDays) VALUES
(N'Aster Components', 'DE', N'Low', 96.50, 45),
(N'Blue Harbor Plastics', 'NL', N'Medium', 92.00, 30),
(N'Caldera MicroWorks', 'US', N'Low', 95.00, 30),
(N'Driftline Fabrication', 'CN', N'High', 88.00, 60),
(N'Echo Valley Packaging', 'PL', N'Low', 97.00, 21);

INSERT INTO ops.Warehouses (WarehouseCode, Region, CapacityUnits) VALUES
(N'AMS-1', N'Europe North', 24000),
(N'WAW-1', N'Europe East', 16000),
(N'RNO-1', N'North America West', 22000);

INSERT INTO ops.Products (SKU, ProductName, Category, StandardCost, ListPrice, ReorderPoint, PreferredVendorID) VALUES
(N'NR-SENSOR-9', N'Cold Chain Sensor v9', N'Electronics', 18.20, 39.00, 900, 3),
(N'NR-VALVE-A2', N'Adaptive Flow Valve A2', N'Industrial', 42.00, 87.00, 420, 4),
(N'NR-INSUL-4', N'Thermal Insulation Panel 4cm', N'Materials', 7.30, 16.00, 1200, 2),
(N'NR-BOX-SMART', N'Smart Returnable Crate', N'Packaging', 11.40, 25.00, 800, 5),
(N'NR-PUMP-MINI', N'Miniature Circulation Pump', N'Industrial', 31.50, 69.00, 350, 4),
(N'NR-GATEWAY-LTE', N'LTE Telemetry Gateway', N'Electronics', 57.00, 129.00, 260, 1);

INSERT INTO ops.InventorySnapshots (ProductID, WarehouseID, SnapshotDate, OnHandUnits, ReservedUnits, DamagedUnits) VALUES
(1, 1, '2026-05-31', 1320, 610, 6),
(1, 3, '2026-05-31', 780, 700, 4),
(2, 1, '2026-05-31', 510, 420, 37),
(2, 2, '2026-05-31', 260, 235, 31),
(3, 1, '2026-05-31', 1900, 620, 18),
(3, 2, '2026-05-31', 760, 540, 14),
(4, 1, '2026-05-31', 1100, 460, 2),
(4, 3, '2026-05-31', 940, 410, 1),
(5, 1, '2026-05-31', 390, 370, 22),
(5, 3, '2026-05-31', 210, 205, 17),
(6, 1, '2026-05-31', 320, 180, 1),
(6, 3, '2026-05-31', 260, 240, 0);

INSERT INTO ops.PurchaseOrders (VendorID, OrderDate, ExpectedDate, ActualReceiptDate, Status) VALUES
(4, '2026-04-02', '2026-04-22', '2026-05-03', N'Received'),
(4, '2026-04-18', '2026-05-08', '2026-05-27', N'Received'),
(4, '2026-05-12', '2026-06-01', NULL, N'Late'),
(3, '2026-05-01', '2026-05-17', '2026-05-16', N'Received'),
(1, '2026-05-03', '2026-05-21', '2026-05-22', N'Received'),
(2, '2026-05-05', '2026-05-24', '2026-05-24', N'Received'),
(5, '2026-05-06', '2026-05-19', '2026-05-18', N'Received');

INSERT INTO ops.PurchaseOrderLines (PurchaseOrderID, ProductID, OrderedUnits, ReceivedUnits, UnitCost) VALUES
(1, 2, 700, 640, 45.50),
(1, 5, 480, 455, 33.20),
(2, 2, 620, 590, 47.10),
(2, 5, 400, 375, 34.40),
(3, 2, 800, 0, 48.00),
(3, 5, 520, 0, 35.10),
(4, 1, 1000, 1000, 18.60),
(5, 6, 360, 360, 58.00),
(6, 3, 1600, 1600, 7.80),
(7, 4, 1400, 1400, 11.60);

INSERT INTO ops.QualityIncidents (ProductID, VendorID, WarehouseID, IncidentDate, Severity, DefectCode, AffectedUnits, RootCause) VALUES
(2, 4, 1, '2026-05-05', N'High', N'PRESSURE_LEAK', 36, N'Valve seal drift after transit heat exposure'),
(5, 4, 3, '2026-05-09', N'Medium', N'NOISY_BEARING', 18, N'Bearing tolerance outside purchase specification'),
(2, 4, 2, '2026-05-29', N'High', N'PRESSURE_LEAK', 29, N'Repeated lot failure, same tooling line'),
(5, 4, 1, '2026-05-30', N'High', N'POWER_SPIKE', 21, N'Controller board substitution by vendor'),
(3, 2, 1, '2026-05-26', N'Low', N'EDGE_CRACK', 12, N'Forklift damage during receiving');

INSERT INTO ops.Customers (CustomerName, Segment, Region, CreditLimit, RelatedVendorID) VALUES
(N'Alpine Grocery Group', N'Enterprise', N'Europe North', 180000, NULL),
(N'Boreal Pharma Logistics', N'Enterprise', N'Europe North', 250000, NULL),
(N'Cirrus Field Labs', N'Midmarket', N'North America West', 90000, NULL),
(N'Driftline Trading HK', N'Distributor', N'Asia Pacific', 75000, 4),
(N'Evergreen Meal Kits', N'Midmarket', N'Europe East', 65000, NULL);

INSERT INTO ops.SalesOrders (CustomerID, OrderDate, RequestedShipDate, ActualShipDate, Status) VALUES
(1, '2026-05-10', '2026-05-18', '2026-05-18', N'Shipped'),
(2, '2026-05-13', '2026-05-23', '2026-05-27', N'Shipped'),
(2, '2026-05-24', '2026-06-03', NULL, N'Blocked'),
(3, '2026-05-15', '2026-05-25', '2026-05-26', N'Shipped'),
(4, '2026-05-16', '2026-05-24', '2026-05-24', N'Shipped'),
(5, '2026-05-21', '2026-05-31', NULL, N'Picking');

INSERT INTO ops.Shipments (SalesOrderID, WarehouseID, Carrier, DepartedAt, DeliveredAt, TemperatureExcursionMinutes, FreightCost, Status) VALUES
(1, 1, N'PolarLine Freight', '2026-05-17T07:15:00', '2026-05-18T10:40:00', 0, 920.00, N'Delivered'),
(2, 1, N'PolarLine Freight', '2026-05-23T16:20:00', '2026-05-27T12:05:00', 155, 1280.00, N'Delivered'),
(3, 1, N'PolarLine Freight', NULL, NULL, 0, 0.00, N'Blocked'),
(4, 3, N'Western Rail Express', '2026-05-24T06:00:00', '2026-05-26T09:30:00', 0, 830.00, N'Delivered'),
(5, 2, N'Azure Sea Forwarding', '2026-05-23T23:10:00', '2026-05-24T22:15:00', 0, 1110.00, N'Delivered'),
(6, 2, N'Vistula Road Link', NULL, NULL, 0, 0.00, N'Picking');

INSERT INTO ops.SalesOrderLines (SalesOrderID, ProductID, OrderedUnits, UnitPrice, DiscountPct) VALUES
(1, 1, 300, 39.00, 2.00),
(1, 3, 500, 16.00, 0.00),
(2, 2, 280, 87.00, 4.00),
(2, 5, 190, 69.00, 4.00),
(3, 2, 420, 87.00, 6.00),
(3, 5, 260, 69.00, 6.00),
(4, 6, 210, 129.00, 3.00),
(5, 2, 120, 87.00, 18.00),
(5, 5, 90, 69.00, 18.00),
(6, 3, 460, 16.00, 2.00),
(6, 4, 320, 25.00, 1.00);

INSERT INTO finance.Payments (CustomerID, SalesOrderID, PaymentDate, Amount, Method, Status) VALUES
(1, 1, '2026-05-20', 19220.00, N'Wire', N'Cleared'),
(2, 2, '2026-05-30', 35933.40, N'Wire', N'Cleared'),
(3, 4, '2026-05-28', 26277.30, N'Card', N'Cleared'),
(4, 5, '2026-05-17', 13615.20, N'Wire', N'Reversed'),
(4, 5, '2026-05-25', 13615.20, N'Wire', N'Cleared');
GO
`
