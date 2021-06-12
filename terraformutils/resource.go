// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package terraformutils

import (
	"fmt"
	"github.com/zclconf/go-cty/cty/convert"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform/addrs"
	"github.com/hashicorp/terraform/states"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"

	"github.com/GoogleCloudPlatform/terraformer/terraformutils/providerwrapper"
)

type Resource struct {
	Address           addrs.Resource
	InstanceState     *states.ResourceInstanceObject // the resource will always contain one instance as terraformer blocks don't use "count" or "for_each"
	Outputs           map[string]*states.OutputValue
	ImportID          string // identifier to be used by terraformer when importing a resource
	Provider          string
	PriorState        map[string]string      // used when refreshing a resource
	IgnoreKeys        []string               `json:",omitempty"`
	AllowEmptyValues  []string               `json:",omitempty"`
	AdditionalFields  map[string]interface{} `json:",omitempty"`
	SlowQueryRequired bool
	DataFiles         map[string][]byte
}

type ApplicableFilter interface {
	IsApplicable(resourceName string) bool
}

type ResourceFilter struct {
	ApplicableFilter
	ServiceName      string
	FieldPath        string
	AcceptableValues []string
}

func (rf *ResourceFilter) Filter(resource Resource) bool {
	if !rf.IsApplicable(strings.TrimPrefix(resource.Address.Type, resource.Provider+"_")) {
		return true
	}
	var vals []interface{}
	switch {
	case rf.FieldPath == "id":
		vals = []interface{}{resource.ImportID}
	case rf.AcceptableValues == nil:
		var dst interface{}
		err := gocty.FromCtyValue(resource.InstanceState.Value, &dst)
		if err != nil {
			log.Println(err.Error())
			return false
		}
		return WalkAndCheckField(rf.FieldPath, dst)
	default:
		var dst interface{}
		err := gocty.FromCtyValue(resource.InstanceState.Value, &dst)
		if err != nil {
			log.Println(err.Error())
			return false
		}
		vals = WalkAndGet(rf.FieldPath, dst)
	}
	for _, val := range vals {
		for _, acceptableValue := range rf.AcceptableValues {
			if val == acceptableValue {
				return true
			}
		}
	}
	return false
}

func (rf *ResourceFilter) IsApplicable(serviceName string) bool {
	return rf.ServiceName == "" || rf.ServiceName == serviceName
}

func (rf *ResourceFilter) isInitial() bool {
	return rf.FieldPath == "id"
}

func NewResource(id, resourceName, resourceType, provider string,
	attributes map[string]string,
	allowEmptyValues []string,
	additionalFields map[string]interface{}) Resource {
	if attributes == nil {
		attributes = map[string]string{}
	}
	attributes["id"] = id // to ensure resource refresh will work well
	return Resource{
		Address: addrs.Resource{
			Mode: addrs.ManagedResourceMode,
			Type: resourceType,
			Name: TfSanitize(resourceName),
		},
		ImportID:         id,
		Provider:         provider,
		PriorState:       attributes,
		AdditionalFields: additionalFields,
		AllowEmptyValues: allowEmptyValues,
	}
}

func NewSimpleResource(id, resourceName, resourceType, provider string, allowEmptyValues []string) Resource {
	return NewResource(
		id,
		resourceName,
		resourceType,
		provider,
		map[string]string{},
		allowEmptyValues,
		map[string]interface{}{},
	)
}

func (r *Resource) Refresh(provider *providerwrapper.ProviderWrapper) {
	var err error
	if r.SlowQueryRequired {
		time.Sleep(200 * time.Millisecond)
	}
	r.InstanceState, err = provider.Refresh(&r.Address, r.PriorState, r.ImportID)
	if err != nil {
		log.Println(err)
	}
}

func (r Resource) GetIDKey() string {
	if _, exist := r.PriorState["self_link"]; exist {
		return "self_link"
	}
	return "id"
}

func (r *Resource) ServiceName() string {
	return strings.TrimPrefix(r.Address.Type, r.Provider+"_")
}

func (r *Resource) HasStateAttr(attr string) bool {
	value := r.InstanceState.Value
	return hasValueAttr(value, attr)
}

func (r *Resource) GetStateAttr(attr string) string {
	if !r.HasStateAttr(attr) {
		return ""
	}
	value := r.InstanceState.Value
	return valueToString(getValueAttr(value, attr))
}

func (r *Resource) GetStateAttrSlice(attr string) []cty.Value {
	if !r.HasStateAttr(attr) {
		return []cty.Value{}
	}
	value := r.InstanceState.Value
	return getValueAttr(value, attr).AsValueSlice()
}

func (r *Resource) GetStateAttrMap(attr string) map[string]cty.Value {
	if !r.HasStateAttr(attr) {
		return map[string]cty.Value{}
	}
	value := r.InstanceState.Value
	return getValueAttr(value, attr).AsValueMap()
}

func (r *Resource) SetStateAttr(attr string, value cty.Value) {
	instanceStateMap := r.InstanceState.Value.AsValueMap()
	instanceStateMap[attr] = value
	r.InstanceState.Value = cty.ObjectVal(instanceStateMap)
}

func (r *Resource) DeleteStateAttr(attr string) {
	instanceStateMap := r.InstanceState.Value.AsValueMap()
	delete(instanceStateMap, attr)
	r.InstanceState.Value = cty.ObjectVal(instanceStateMap)
}

func (r *Resource) SortStateAttrStringSlice(attr string) {
	if r.HasStateAttr(attr) {
		var sortedStrings []string
		for _, v := range r.GetStateAttrSlice(attr) {
			sortedStrings = append(sortedStrings, v.AsString())
		}
		sort.Strings(sortedStrings)
		var sortedValues []cty.Value
		for _, v := range sortedStrings {
			sortedValues = append(sortedValues, cty.StringVal(v))
		}
		r.SetStateAttr(attr, ListToValue(sortedValues))
	}
}

func (r *Resource) HasStateAttrFirstAttr(firstAttr string, secondAttr string) bool {
	if r.HasStateAttr(firstAttr) {
		value := r.GetStateAttrSlice(firstAttr)[0]
		return hasValueAttr(value, secondAttr)
	} else {
		return false
	}
}

func (r *Resource) GetStateAttrFirstAttr(firstAttr string, secondAttr string) string {
	if !r.HasStateAttrFirstAttr(firstAttr, secondAttr) {
		return ""
	}
	return valueToString(getValueAttr(r.GetStateAttrSlice(firstAttr)[0], secondAttr))
}

func (r *Resource) GetStateAttrFirstAttrMap(firstAttr string, secondAttr string) map[string]cty.Value {
	if !r.HasStateAttrFirstAttr(firstAttr, secondAttr) {
		return map[string]cty.Value{}
	}
	return getValueAttr(r.GetStateAttrSlice(firstAttr)[0], secondAttr).AsValueMap()
}

func (r *Resource) DeleteStateAttrFirstAttr(firstAttr string, secondAttr string) {
	instanceStateMap := r.InstanceState.Value.AsValueMap()
	firstAttrMap := instanceStateMap[firstAttr].AsValueSlice()[0].AsValueMap()
	delete(firstAttrMap, secondAttr)
	instanceStateMap[firstAttr] = ListToValue([]cty.Value{cty.ObjectVal(firstAttrMap)})
	r.InstanceState.Value = cty.ObjectVal(instanceStateMap)
}

func (r *Resource) SetStateAttrFirstAttr(firstAttr string, secondAttr string, val cty.Value) {
	instanceStateMap := r.InstanceState.Value.AsValueMap()
	firstAttrMap := instanceStateMap[firstAttr].AsValueSlice()[0].AsValueMap()
	firstAttrMap[secondAttr] = val
	instanceStateMap[firstAttr] = ListToValue([]cty.Value{cty.ObjectVal(firstAttrMap)})
	r.InstanceState.Value = cty.ObjectVal(instanceStateMap)
}

func (r *Resource) SortStateAttrEachAttrStringSlice(firstAttr string, secondAttr string) {
	if r.HasStateAttr(firstAttr) {
		firstAttrSlice := r.GetStateAttrSlice(firstAttr)
		for i, firstAttrSliceItem := range firstAttrSlice {
			if hasValueAttr(firstAttrSliceItem, secondAttr) {
				continue
			}
			secondAttrSlice := getValueAttr(firstAttrSliceItem, secondAttr).AsValueSlice()
			var sortedSecondAttrSliceStrings []string
			for _, secondAttrSliceString := range secondAttrSlice {
				sortedSecondAttrSliceStrings = append(sortedSecondAttrSliceStrings, secondAttrSliceString.AsString())
			}
			sort.Strings(sortedSecondAttrSliceStrings)
			var sortedSecondAttrSliceValues []cty.Value
			for _, ssl := range sortedSecondAttrSliceStrings {
				sortedSecondAttrSliceValues = append(sortedSecondAttrSliceValues, cty.StringVal(ssl))
			}
			valueMap := firstAttrSliceItem.AsValueMap()
			valueMap[secondAttr] = ListToValue(sortedSecondAttrSliceValues)
			firstAttrSlice[i] = cty.ObjectVal(valueMap)
		}
		r.SetStateAttr(firstAttr, ListToValue(firstAttrSlice))
	}
}

func ListToValue(value []cty.Value) cty.Value {
	if len(value) == 0 {
		return cty.ListValEmpty(cty.EmptyObject)
	} else {
		return ListVal(value)
	}
}

func ListVal(vals []cty.Value) cty.Value {

	if vals[0].Type().IsMapType() || vals[0].Type().IsObjectType() {
		vals[0].Type().AttributeTypes()
		types := make([]cty.Type, len(vals))
		for i, val := range vals {
			types[i] = val.Type()
		}
		unifiedType, _ := convert.Unify(types)

		convertedItems := make([]cty.Value, len(vals))
		for i, val := range vals {
			value, err := convert.Convert(val, unifiedType)
			if err != nil {
				panic(err)
			}
			convertedItems[i] = value
		}

		return cty.ListVal(convertedItems)

	} else {
		return cty.ListVal(vals)
	}
}

func hasValueAttr(value cty.Value, attr string) bool {
	if value.Type().IsObjectType() {
		return value.Type().HasAttribute(attr) && !value.GetAttr(attr).IsNull()
	} else {
		return value.HasIndex(cty.StringVal(attr)) == cty.True && !value.Index(cty.StringVal(attr)).IsNull()
	}
}

func getValueAttr(value cty.Value, attr string) cty.Value {
	if value.Type().IsObjectType() {
		return value.GetAttr(attr)
	} else {
		return value.Index(cty.StringVal(attr))
	}
}

func valueToString(val cty.Value) string {
	switch val.Type() {
	case cty.String:
		return val.AsString()
	case cty.Number:
		fv := val.AsBigFloat()
		if fv.IsInt() {
			intVal, _ := fv.Int64()
			return strconv.FormatInt(intVal, 10)
		} else {
			return fmt.Sprintf("%f", fv)
		}
	default:
		return val.GoString()
	}
}
