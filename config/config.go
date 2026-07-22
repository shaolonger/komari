package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ConfigItem struct {
	Key   string `gorm:"primaryKey;column:key;type:text"`
	Value string `gorm:"column:value;type:text"` // 存 JSON 字符串
}

func (ConfigItem) TableName() string {
	return "configs"
}

var (
	db      *gorm.DB
	writeMu sync.Mutex
	current atomic.Pointer[configSnapshot]
	SetDb   = func(gdb *gorm.DB) {
		writeMu.Lock()
		defer writeMu.Unlock()
		db = gdb
		migrateInPlace()
		snapshot, err := loadSnapshot(gdb)
		if err != nil {
			panic("load config snapshot: " + err.Error())
		}
		current.Store(snapshot)
	}
)

func Ready() bool {
	return db != nil && current.Load() != nil
}

type configSnapshot struct {
	raw    map[string]string
	values map[string]any
}

func loadSnapshot(gdb *gorm.DB) (*configSnapshot, error) {
	var items []ConfigItem
	if err := gdb.Find(&items).Error; err != nil {
		return nil, err
	}
	snapshot := &configSnapshot{raw: make(map[string]string, len(items)), values: make(map[string]any, len(items))}
	for _, item := range items {
		snapshot.raw[item.Key] = item.Value
		var value any
		if err := json.Unmarshal([]byte(item.Value), &value); err == nil {
			snapshot.values[item.Key] = value
		}
	}
	return snapshot, nil
}

func emptySnapshot() *configSnapshot {
	return &configSnapshot{raw: map[string]string{}, values: map[string]any{}}
}

func snapshotNow() *configSnapshot {
	if snapshot := current.Load(); snapshot != nil {
		return snapshot
	}
	return emptySnapshot()
}

func cloneSnapshot(source *configSnapshot, additional int) *configSnapshot {
	next := &configSnapshot{
		raw:    make(map[string]string, len(source.raw)+additional),
		values: make(map[string]any, len(source.values)+additional),
	}
	for key, value := range source.raw {
		next.raw[key] = value
	}
	for key, value := range source.values {
		next.values[key] = value
	}
	return next
}

func migrateInPlace() {
	if db.Migrator().HasTable("configs") && db.Migrator().HasColumn(&Legacy{}, "Sitename") {
		slog.Info("[>1.1.4] Moving legacy config data...")

		var oldData Legacy
		if err := db.Order("id desc").First(&oldData).Error; err != nil {
			db.Migrator().DropTable("configs")
		} else {
			var newRows []ConfigItem
			val := reflect.ValueOf(oldData)
			typ := reflect.TypeOf(oldData)

			for i := 0; i < val.NumField(); i++ {
				field := typ.Field(i)
				tag := field.Tag.Get("json")
				key := strings.Split(tag, ",")[0]

				// 过滤 id 和无用字段
				if key == "" || key == "-" || key == "id" {
					continue
				}

				valInterface := val.Field(i).Interface()
				jsonBytes, _ := json.Marshal(valInterface)

				newRows = append(newRows, ConfigItem{
					Key:   key,
					Value: string(jsonBytes),
				})
			}

			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Migrator().DropTable("configs"); err != nil {
					return err
				}
				if err := tx.AutoMigrate(&ConfigItem{}); err != nil {
					return err
				}
				if len(newRows) > 0 {
					return tx.Create(&newRows).Error
				}
				return nil
			})

			if err != nil {
				panic("failed " + err.Error())
			}
			return
		}
	}

	db.AutoMigrate(&ConfigItem{})
}

// Get 获取原始值 (反序列化为 interface{})
func Get(key string, defaul ...any) (any, error) {
	snapshot := snapshotNow()
	if _, ok := snapshot.raw[key]; ok {
		value, found := snapshot.values[key]
		if !found {
			return nil, fmt.Errorf("invalid JSON for config %q", key)
		}
		return cloneConfigValue(value), nil
	}
	if len(defaul) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	value, err := setDefault(key, defaul[0])
	return cloneConfigValue(value), err
}

// GetAs 获取并转换为指定类型 (泛型)，支持数值类型自动转换
func GetAs[T any](key string, defaul ...any) (T, error) {
	var t T
	snapshot := snapshotNow()
	value, found := snapshot.values[key]
	if !found {
		if _, exists := snapshot.raw[key]; exists {
			return t, fmt.Errorf("invalid JSON for config %q", key)
		}
		if len(defaul) == 0 {
			return t, gorm.ErrRecordNotFound
		}
		converted := reflect.ValueOf(&t).Elem()
		if err := convertAndSet(defaul[0], converted); err != nil {
			return t, fmt.Errorf("default value type mismatch: expected %T, got %T", t, defaul[0])
		}
		stored, err := setDefault(key, converted.Interface())
		if err != nil {
			return t, err
		}
		value = stored
	}
	if cast, ok := value.(T); ok {
		return cloneTyped(cast), nil
	}
	if err := convertAndSet(value, reflect.ValueOf(&t).Elem()); err != nil {
		return t, err
	}
	return t, nil
}

// GetMany 获取多个配置项，keys 为 map[key]defaultValue
// 如果 defaultValue 为 nil，则数据库不存在时不写入
// 如果 defaultValue 不为 nil，则数据库不存在时写入默认值
func GetMany(keys map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(keys))
	snapshot := snapshotNow()
	missing := make(map[string]any)
	for key, defaultValue := range keys {
		if value, ok := snapshot.values[key]; ok {
			result[key] = cloneConfigValue(value)
		} else if _, invalid := snapshot.raw[key]; invalid {
			return nil, fmt.Errorf("invalid JSON for config %q", key)
		} else if defaultValue != nil {
			missing[key] = defaultValue
		}
	}
	if len(missing) > 0 {
		stored, err := setDefaults(missing)
		if err != nil {
			return nil, err
		}
		for key, value := range stored {
			result[key] = cloneConfigValue(value)
		}
	}
	return result, nil
}

// GetManyAs 将多个配置项映射到一个结构体中，json tag 作为 Key
// 支持 default tag 作为默认值，如果数据库中不存在且有 default tag 则写入数据库
// 没有 default tag 的字段使用零值，不写入数据库
func GetManyAs[T any]() (*T, error) {
	var t T
	val := reflect.ValueOf(&t).Elem()
	typ := val.Type()

	type fieldInfo struct {
		index      int
		key        string
		hasDefault bool
		defaultVal string
	}

	fields := make([]fieldInfo, 0)

	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		// 解析 json tag，处理 "key,omitempty" 格式
		key := strings.Split(jsonTag, ",")[0]
		if key == "" || key == "-" {
			continue
		}

		defaultTag := field.Tag.Get("default")
		hasDefault := defaultTag != "" || field.Tag.Get("default") == ""
		// 检查是否显式定义了 default tag (即使值为空)
		_, hasDefault = field.Tag.Lookup("default")

		fields = append(fields, fieldInfo{
			index:      i,
			key:        key,
			hasDefault: hasDefault,
			defaultVal: defaultTag,
		})
	}

	if len(fields) == 0 {
		return &t, nil
	}
	snapshot := snapshotNow()
	defaults := make(map[string]any)

	for _, fi := range fields {
		fieldVal := val.Field(fi.index)
		if !fieldVal.CanSet() {
			continue
		}

		if value, found := snapshot.values[fi.key]; found {
			if err := convertAndSet(value, fieldVal); err != nil {
				slog.Warn("unmarshal config failed", "key", fi.key, "error", err)
			}
		} else if _, invalid := snapshot.raw[fi.key]; invalid {
			slog.Warn("unmarshal config failed", "key", fi.key, "error", "invalid JSON")
		} else if fi.hasDefault {
			// 数据库中不存在，但有 default tag，解析默认值并写入数据库
			if err := parseDefaultToField(fi.defaultVal, fieldVal); err != nil {
				slog.Warn("parse default value failed", "key", fi.key, "error", err)
				continue
			}
			defaults[fi.key] = fieldVal.Interface()
		}
	}
	if len(defaults) > 0 {
		stored, err := setDefaults(defaults)
		if err != nil {
			return nil, err
		}
		for _, fi := range fields {
			value, ok := stored[fi.key]
			if !ok {
				continue
			}
			if err := convertAndSet(value, val.Field(fi.index)); err != nil {
				return nil, err
			}
		}
	}
	return &t, nil
}

func setDefault(key string, value any) (any, error) {
	stored, err := setDefaults(map[string]any{key: value})
	if err != nil {
		return nil, err
	}
	return stored[key], nil
}

func setDefaults(defaults map[string]any) (map[string]any, error) {
	type preparedDefault struct {
		item  ConfigItem
		value any
	}
	prepared := make(map[string]preparedDefault, len(defaults))
	for key, value := range defaults {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("marshal default config %s: %w", key, err)
		}
		prepared[key] = preparedDefault{item: ConfigItem{Key: key, Value: string(encoded)}, value: cloneStoredValue(value)}
	}

	writeMu.Lock()
	defer writeMu.Unlock()
	snapshot := snapshotNow()
	result := make(map[string]any, len(defaults))
	items := make([]ConfigItem, 0, len(defaults))
	newValues := make(map[string]any)
	for key, candidate := range prepared {
		if existing, ok := snapshot.values[key]; ok {
			result[key] = existing
			continue
		}
		if _, exists := snapshot.raw[key]; exists {
			return nil, fmt.Errorf("invalid JSON for config %q", key)
		}
		items = append(items, candidate.item)
		newValues[key] = candidate.value
		result[key] = candidate.value
	}
	if len(items) == 0 {
		return result, nil
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&items).Error
	}); err != nil {
		return nil, err
	}
	next := cloneSnapshot(snapshot, len(items))
	for _, item := range items {
		next.raw[item.Key] = item.Value
		next.values[item.Key] = newValues[item.Key]
	}
	current.Store(next)
	publishEvent(nil, newValues)
	return result, nil
}

func cloneConfigValue(value any) any {
	if value == nil {
		return nil
	}
	kind := reflect.TypeOf(value).Kind()
	if kind != reflect.Map && kind != reflect.Slice && kind != reflect.Array && kind != reflect.Pointer && kind != reflect.Struct {
		return value
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var clone any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return value
	}
	return clone
}

func cloneStoredValue(value any) any {
	if value == nil {
		return nil
	}
	typeOf := reflect.TypeOf(value)
	kind := typeOf.Kind()
	if kind != reflect.Map && kind != reflect.Slice && kind != reflect.Array && kind != reflect.Pointer && kind != reflect.Struct {
		return value
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	target := reflect.New(typeOf)
	if err := json.Unmarshal(encoded, target.Interface()); err != nil {
		return value
	}
	return target.Elem().Interface()
}

func cloneTyped[T any](value T) T {
	typeOf := reflect.TypeOf(value)
	if typeOf == nil {
		return value
	}
	kind := typeOf.Kind()
	if kind != reflect.Map && kind != reflect.Slice && kind != reflect.Array && kind != reflect.Pointer && kind != reflect.Struct {
		return value
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var clone T
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return value
	}
	return clone
}

// unmarshalToField 将 JSON 字符串反序列化到字段，支持数值类型转换
func unmarshalToField(jsonStr string, fieldVal reflect.Value) error {
	target := reflect.New(fieldVal.Type()).Interface()
	if err := json.Unmarshal([]byte(jsonStr), target); err != nil {
		// 尝试通用解析后转换
		var generic any
		if err := json.Unmarshal([]byte(jsonStr), &generic); err != nil {
			return err
		}
		return convertAndSet(generic, fieldVal)
	}
	fieldVal.Set(reflect.ValueOf(target).Elem())
	return nil
}

// parseDefaultToField 解析 default tag 值到字段
func parseDefaultToField(defaultVal string, fieldVal reflect.Value) error {
	kind := fieldVal.Kind()

	switch kind {
	case reflect.String:
		fieldVal.SetString(defaultVal)
	case reflect.Bool:
		fieldVal.SetBool(defaultVal == "true" || defaultVal == "1")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var v int64
		if defaultVal != "" {
			if _, err := fmt.Sscanf(defaultVal, "%d", &v); err != nil {
				// 尝试解析浮点数后转换
				var f float64
				if _, err := fmt.Sscanf(defaultVal, "%f", &f); err != nil {
					return err
				}
				v = int64(f)
			}
		}
		fieldVal.SetInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var v uint64
		if defaultVal != "" {
			if _, err := fmt.Sscanf(defaultVal, "%d", &v); err != nil {
				var f float64
				if _, err := fmt.Sscanf(defaultVal, "%f", &f); err != nil {
					return err
				}
				v = uint64(f)
			}
		}
		fieldVal.SetUint(v)
	case reflect.Float32, reflect.Float64:
		var v float64
		if defaultVal != "" {
			if _, err := fmt.Sscanf(defaultVal, "%f", &v); err != nil {
				return err
			}
		}
		fieldVal.SetFloat(v)
	default:
		// 对于复杂类型，尝试 JSON 解析
		if defaultVal == "" {
			return nil // 保持零值
		}
		target := reflect.New(fieldVal.Type()).Interface()
		if err := json.Unmarshal([]byte(defaultVal), target); err != nil {
			return err
		}
		fieldVal.Set(reflect.ValueOf(target).Elem())
	}
	return nil
}

// convertAndSet 通用类型转换并设置字段值
func convertAndSet(val any, fieldVal reflect.Value) error {
	if val == nil {
		return nil
	}

	targetType := fieldVal.Type()
	v := reflect.ValueOf(val)

	// 直接类型匹配
	if v.Type().AssignableTo(targetType) {
		fieldVal.Set(v)
		return nil
	}

	// 类型可转换
	if v.Type().ConvertibleTo(targetType) {
		fieldVal.Set(v.Convert(targetType))
		return nil
	}

	// 数值类型特殊处理 (JSON 数字默认解析为 float64)
	if f, ok := val.(float64); ok {
		switch fieldVal.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			fieldVal.SetInt(int64(f))
			return nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			fieldVal.SetUint(uint64(f))
			return nil
		case reflect.Float32, reflect.Float64:
			fieldVal.SetFloat(f)
			return nil
		}
	}

	// JSON 回环转换
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	target := reflect.New(targetType).Interface()
	if err := json.Unmarshal(b, target); err != nil {
		return err
	}
	fieldVal.Set(reflect.ValueOf(target).Elem())
	return nil
}

func GetAll() (map[string]any, error) {
	snapshot := snapshotNow()
	result := make(map[string]any, len(snapshot.values))
	for key, value := range snapshot.values {
		result[key] = cloneConfigValue(value)
	}
	return result, nil
}

// Set 设置单个配置
func Set(key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return applyChanges(map[string]encodedConfigValue{key: {raw: string(encoded), value: value}})
}

// SetMany 将结构体保存为多个配置项
func SetManyAs[T any](config T) error {
	val := reflect.ValueOf(config)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	typ := val.Type()
	changes := make(map[string]encodedConfigValue)

	for i := 0; i < val.NumField(); i++ {
		fieldType := typ.Field(i)
		tag := strings.Split(fieldType.Tag.Get("json"), ",")[0]

		if tag == "" || tag == "-" {
			continue
		}

		fieldValue := val.Field(i).Interface()

		bytes, err := json.Marshal(fieldValue)
		if err != nil {
			return fmt.Errorf("marshal field %s failed: %w", fieldType.Name, err)
		}

		changes[tag] = encodedConfigValue{raw: string(bytes), value: fieldValue}
	}
	return applyChanges(changes)
}

func SetMany(cst map[string]any) error {
	changes := make(map[string]encodedConfigValue, len(cst))
	for k, v := range cst {
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshal key %s failed: %w", k, err)
		}
		changes[k] = encodedConfigValue{raw: string(encoded), value: v}
	}
	return applyChanges(changes)
}

type encodedConfigValue struct {
	raw   string
	value any
}

func applyChanges(changes map[string]encodedConfigValue) error {
	if len(changes) == 0 {
		return nil
	}
	writeMu.Lock()
	snapshot := snapshotNow()
	items := make([]ConfigItem, 0, len(changes))
	oldValues := make(map[string]any, len(changes))
	newValues := make(map[string]any, len(changes))
	for key, change := range changes {
		items = append(items, ConfigItem{Key: key, Value: change.raw})
		if old, ok := snapshot.values[key]; ok {
			oldValues[key] = old
		}
		newValues[key] = cloneStoredValue(change.value)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).Create(&items).Error
	})
	if err != nil {
		writeMu.Unlock()
		return err
	}
	next := cloneSnapshot(snapshot, len(changes))
	for key, change := range changes {
		next.raw[key] = change.raw
		next.values[key] = newValues[key]
	}
	current.Store(next)
	writeMu.Unlock()
	publishEvent(oldValues, newValues)
	return nil
}

type ConfigEvent struct {
	Old map[string]any // Old models.Config
	New map[string]any // New models.Config
}

func (e ConfigEvent) IsChanged(key string) bool {
	oldVal, oldOk := e.Old[key]
	newVal, newOk := e.New[key]
	if !oldOk && !newOk {
		return false
	}
	if oldOk != newOk {
		return true
	}
	return !reflect.DeepEqual(oldVal, newVal)
}

func IsChangedT[T any](e ConfigEvent, key string) (bool, T) {
	changed := e.IsChanged(key)
	var zero T

	val, ok := e.New[key]
	if !ok {
		val, ok = e.Old[key]
		if !ok {
			return changed, zero
		}
	}
	if val == nil {
		return changed, zero
	}

	// Fast path: direct assertion.
	if cast, ok := val.(T); ok {
		return changed, cast
	}

	// Try reflection-based conversion (covers numeric conversions, etc.).
	targetType := reflect.TypeOf((*T)(nil)).Elem()
	v := reflect.ValueOf(val)
	if v.IsValid() {
		if v.Type().AssignableTo(targetType) {
			return changed, v.Interface().(T)
		}
		if v.Type().ConvertibleTo(targetType) {
			converted := v.Convert(targetType)
			return changed, converted.Interface().(T)
		}
	}

	// Fallback: JSON roundtrip for map/struct and other loosely typed values.
	if b, err := json.Marshal(val); err == nil {
		var out T
		if err := json.Unmarshal(b, &out); err == nil {
			return changed, out
		}
	}

	return changed, zero
}

// ConfigSubscriber handles config events
type ConfigSubscriber func(event ConfigEvent)

var (
	subscribersMu sync.RWMutex
	subscribers   []ConfigSubscriber
)

// Subscribe registers a subscriber for all config events.
func Subscribe(subscriber ConfigSubscriber) {
	subscribersMu.Lock()
	defer subscribersMu.Unlock()
	subscribers = append(subscribers, subscriber)
}

// publishEvent notifies all subscribers of a config change.
func publishEvent(oldVal, newVal map[string]any) {
	subscribersMu.RLock()
	currentSubscribers := append([]ConfigSubscriber(nil), subscribers...)
	subscribersMu.RUnlock()
	for _, sub := range currentSubscribers {
		event := ConfigEvent{Old: cloneConfigMap(oldVal), New: cloneConfigMap(newVal)}
		go sub(event)
	}
}

func cloneConfigMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	clone := make(map[string]any, len(values))
	for key, value := range values {
		clone[key] = cloneConfigValue(value)
	}
	return clone
}
