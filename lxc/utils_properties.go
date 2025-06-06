package main

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"

	"github.com/canonical/lxd/shared/i18n"
)

// stringToTimeHookFunc is a custom decoding hook that converts string values to time.Time using the given layout.
func stringToTimeHookFunc(layout string) mapstructure.DecodeHookFuncType {
	return func(from reflect.Type, to reflect.Type, data any) (any, error) {
		if from.Kind() == reflect.String && to == reflect.TypeOf(time.Time{}) {
			strValue, ok := data.(string)
			if !ok {
				return nil, fmt.Errorf("Expected string, got %T", data)
			}

			t, err := time.Parse(layout, strValue)
			if err != nil {
				return nil, err
			}

			return t, nil
		}

		return data, nil
	}
}

// stringToBoolHookFunc is a custom decoding hook that converts string values to bool.
func stringToBoolHookFunc() mapstructure.DecodeHookFunc {
	return func(f reflect.Kind, t reflect.Kind, data any) (any, error) {
		if f != reflect.String || t != reflect.Bool {
			return data, nil
		}

		str, ok := data.(string)
		if !ok {
			return data, fmt.Errorf("Expected string, got %T", data)
		}

		str = strings.ToLower(str)
		switch str {
		case "1", "t", "true":
			return true, nil
		case "0", "f", "false":
			return false, nil
		default:
			return false, fmt.Errorf("Invalid boolean value: %s", str)
		}
	}
}

// stringToIntHookFunc is a custom decoding hook that converts string values to int.
func stringToIntHookFunc() mapstructure.DecodeHookFunc {
	return func(f reflect.Kind, t reflect.Kind, data any) (any, error) {
		if f != reflect.String || (t != reflect.Int && t != reflect.Int8 && t != reflect.Int16 && t != reflect.Int32 && t != reflect.Int64) {
			return data, nil
		}

		str, ok := data.(string)
		if !ok {
			return data, fmt.Errorf("Expected string, got %T", data)
		}

		value, err := strconv.Atoi(str)
		if err != nil {
			return data, err
		}

		return value, nil
	}
}

// stringToFloatHookFunc is a custom decoding hook that converts string values to float.
func stringToFloatHookFunc() mapstructure.DecodeHookFunc {
	return func(f reflect.Kind, t reflect.Kind, data any) (any, error) {
		if f != reflect.String || (t != reflect.Float32 && t != reflect.Float64) {
			return data, nil
		}

		str, ok := data.(string)
		if !ok {
			return data, fmt.Errorf("Expected string, got %T", data)
		}

		value, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return data, err
		}

		return value, nil
	}
}

// getFieldByJSONTag gets the value of a struct field by its JSON tag.
func getFieldByJSONTag(obj any, tag string) (any, error) {
	var res any
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("Expected a struct, got a %v", v.Kind())
	}

	ok, res := getFromStruct(v, tag)
	if !ok {
		return nil, fmt.Errorf("The property with tag %q does not exist", tag)
	}

	return res, nil
}

// getFromStruct scans a struct for a field with the given JSON tag, including fields of inline structs.
func getFromStruct(v reflect.Value, tag string) (bool, any) {
	for i := range v.NumField() {
		field := v.Field(i)
		jsonTag := v.Type().Field(i).Tag.Get("json")

		// Ignore any options that might be specified after a comma in the tag.
		commaIdx := strings.Index(jsonTag, ",")
		if commaIdx > 0 {
			jsonTag = jsonTag[:commaIdx]
		}

		if strings.EqualFold(jsonTag, tag) {
			return true, field.Interface()
		}

		if v.Type().Field(i).Anonymous {
			if field.Kind() == reflect.Ptr {
				field = field.Elem()
			}

			if field.Kind() == reflect.Struct {
				ok, res := getFromStruct(field, tag)
				if ok {
					return ok, res
				}
			}
		}
	}

	return false, nil
}

// setFieldByJSONTag sets the value of a struct field by its JSON tag.
func setFieldByJSONTag(obj any, tag string, value any) {
	v := reflect.ValueOf(obj).Elem()
	var fieldName string

	for i := range v.NumField() {
		jsonTag := v.Type().Field(i).Tag.Get("json")
		commaIdx := strings.Index(jsonTag, ",")
		if commaIdx > 0 {
			jsonTag = jsonTag[:commaIdx]
		}

		if strings.EqualFold(jsonTag, tag) {
			fieldName = v.Type().Field(i).Name
		}
	}

	if fieldName != "" {
		if v.FieldByName(fieldName).CanSet() {
			v.FieldByName(fieldName).Set(reflect.ValueOf(value))
		}
	}
}

// unsetFieldByJSONTag unsets (give a default value) the value of a struct field by its JSON tag.
func unsetFieldByJSONTag(obj any, tag string) error {
	v, err := getFieldByJSONTag(obj, tag)
	if err != nil {
		return err
	}

	switch v.(type) {
	case string:
		setFieldByJSONTag(obj, tag, "")
	case int:
		setFieldByJSONTag(obj, tag, 0)
	case bool:
		setFieldByJSONTag(obj, tag, false)
	case float32, float64:
		setFieldByJSONTag(obj, tag, 0.0)
	case time.Time:
		setFieldByJSONTag(obj, tag, time.Time{})
	case *time.Time:
		setFieldByJSONTag(obj, tag, &time.Time{})
	}

	return nil
}

// unpackKVToWritable unpacks a map[string]string into a writable API struct.
func unpackKVToWritable(writable any, keys map[string]string) error {
	data := make(map[string]any)
	for k, v := range keys {
		data[k] = v
	}

	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName: "json",
		Result:  writable,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			stringToBoolHookFunc(),
			stringToIntHookFunc(),
			stringToFloatHookFunc(),
			stringToTimeHookFunc(time.RFC3339),
		),
	})
	if err != nil {
		return fmt.Errorf(i18n.G("Error creating decoder: %v"), err)
	}

	err = decoder.Decode(data)
	if err != nil {
		return fmt.Errorf(i18n.G("Error decoding data: %v"), err)
	}

	return nil
}
