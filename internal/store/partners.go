package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"icewine-erp/internal/model"
)

// ---------------------------------------------------------------- 供应商

const supplierCols = `id, name, contact_name, phone, email, country, address, notes, is_active, created_at`

func scanSupplier(row interface{ Scan(...any) error }) (*model.Supplier, error) {
	var (
		x        model.Supplier
		isActive int64
	)
	if err := row.Scan(&x.ID, &x.Name, &x.ContactName, &x.Phone, &x.Email, &x.Country,
		&x.Address, &x.Notes, &isActive, &x.CreatedAt); err != nil {
		return nil, err
	}
	x.IsActive = i2b(isActive)
	return &x, nil
}

// ListSuppliers 供应商列表。
func (s *Store) ListSuppliers(ctx context.Context, keyword string, includeInactive bool) ([]model.Supplier, error) {
	var (
		where []string
		args  []any
	)
	if !includeInactive {
		where = append(where, "is_active = 1")
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		where = append(where, "(name LIKE ? OR contact_name LIKE ? OR phone LIKE ? OR country LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like, like, like)
	}
	query := `SELECT ` + supplierCols + ` FROM suppliers`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY is_active DESC, name"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Supplier
	for rows.Next() {
		x, err := scanSupplier(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// SupplierByID 按 ID 查询供应商。
func (s *Store) SupplierByID(ctx context.Context, id int64) (*model.Supplier, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+supplierCols+` FROM suppliers WHERE id = ?`, id)
	x, err := scanSupplier(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return x, err
}

// CreateSupplier 新建供应商。
func (s *Store) CreateSupplier(ctx context.Context, x *model.Supplier) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO suppliers(name, contact_name, phone, email, country, address, notes, is_active, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.Name, x.ContactName, x.Phone, x.Email, x.Country, x.Address, x.Notes, b2i(x.IsActive), Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateSupplier 更新供应商。
func (s *Store) UpdateSupplier(ctx context.Context, x *model.Supplier) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE suppliers SET name = ?, contact_name = ?, phone = ?, email = ?,
		        country = ?, address = ?, notes = ?, is_active = ? WHERE id = ?`,
		x.Name, x.ContactName, x.Phone, x.Email, x.Country, x.Address, x.Notes, b2i(x.IsActive), x.ID)
	return err
}

// DeleteSupplier 删除供应商；若有采购单引用则拒绝。
func (s *Store) DeleteSupplier(ctx context.Context, id int64) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM purchases WHERE supplier_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("该供应商已关联 %d 张采购单，不能删除；请改为「停用」", n)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE products SET supplier_id = NULL WHERE supplier_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM suppliers WHERE id = ?`, id)
	return err
}

// CountSuppliers 供应商数量。
func (s *Store) CountSuppliers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppliers WHERE is_active = 1`).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------- 客户

const customerCols = `id, name, phone, email, address, notes, is_active, created_at`

func scanCustomer(row interface{ Scan(...any) error }) (*model.Customer, error) {
	var (
		x        model.Customer
		isActive int64
	)
	if err := row.Scan(&x.ID, &x.Name, &x.Phone, &x.Email, &x.Address,
		&x.Notes, &isActive, &x.CreatedAt); err != nil {
		return nil, err
	}
	x.IsActive = i2b(isActive)
	return &x, nil
}

// ListCustomers 客户列表。
func (s *Store) ListCustomers(ctx context.Context, keyword string, includeInactive bool) ([]model.Customer, error) {
	var (
		where []string
		args  []any
	)
	if !includeInactive {
		where = append(where, "is_active = 1")
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		where = append(where, "(name LIKE ? OR phone LIKE ? OR email LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like, like)
	}
	query := `SELECT ` + customerCols + ` FROM customers`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY is_active DESC, name"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Customer
	for rows.Next() {
		x, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// CustomerByID 按 ID 查询客户。
func (s *Store) CustomerByID(ctx context.Context, id int64) (*model.Customer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+customerCols+` FROM customers WHERE id = ?`, id)
	x, err := scanCustomer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return x, err
}

// CreateCustomer 新建客户。
func (s *Store) CreateCustomer(ctx context.Context, x *model.Customer) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO customers(name, phone, email, address, notes, is_active, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		x.Name, x.Phone, x.Email, x.Address, x.Notes, b2i(x.IsActive), Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateCustomer 更新客户。
func (s *Store) UpdateCustomer(ctx context.Context, x *model.Customer) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE customers SET name = ?, phone = ?, email = ?, address = ?, notes = ?, is_active = ?
		 WHERE id = ?`,
		x.Name, x.Phone, x.Email, x.Address, x.Notes, b2i(x.IsActive), x.ID)
	return err
}

// DeleteCustomer 删除客户。
func (s *Store) DeleteCustomer(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM customers WHERE id = ?`, id)
	return err
}

// CountCustomers 客户数量。
func (s *Store) CountCustomers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM customers WHERE is_active = 1`).Scan(&n)
	return n, err
}
