/*
 * Copyright 2012-2019 the original author or authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package SpringCore

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/go-spring/go-spring-parent/spring-logger"
	"github.com/go-spring/go-spring-parent/spring-utils"
)

// beanKey Bean's unique key, type+name.
type beanKey struct {
	rType reflect.Type
	name  string
}

// beanCacheItem BeanCache's item.
type beanCacheItem struct {
	beans []*BeanDefinition
}

// newBeanCacheItem beanCacheItem 的构造函数
func newBeanCacheItem() *beanCacheItem {
	return &beanCacheItem{
		beans: make([]*BeanDefinition, 0),
	}
}

func (item *beanCacheItem) store(t reflect.Type, bd *BeanDefinition) {
	SpringLogger.Debugf("register bean type:\"%s\" beanId:\"%s\" %s", t.String(), bd.BeanId(), bd.Caller())
	item.beans = append(item.beans, bd)
}

// wiringStack 存储绑定中的 Bean
type wiringStack struct {
	stack   *list.List
	watcher []WiringWatcher
}

// newWiringStack wiringStack 的构造函数
func newWiringStack(watcher []WiringWatcher) *wiringStack {

	if len(watcher) == 0 { // 添加默认的注入监视器
		watcher = append(watcher, func(bd IBeanDefinition, event WiringEvent) {
			switch event {
			case WiringEvent_Push:
				SpringLogger.Tracef("wiring %s", bd.Description())
			case WiringEvent_Pop:
				SpringLogger.Tracef("wired %s", bd.Description())
			}
		})
	}

	return &wiringStack{
		stack:   list.New(),
		watcher: watcher,
	}
}

// pushBack 添加一个 Item 到尾部
func (s *wiringStack) pushBack(bd IBeanDefinition) {
	s.stack.PushBack(bd)

	for _, w := range s.watcher {
		w(bd, WiringEvent_Push)
	}
}

// popBack 删除尾部的 item
func (s *wiringStack) popBack() {
	e := s.stack.Remove(s.stack.Back())

	for _, w := range s.watcher {
		w(e.(IBeanDefinition), WiringEvent_Pop)
	}
}

// path 返回依赖注入的路径
func (s *wiringStack) path() (path string) {
	for e := s.stack.Front(); e != nil; e = e.Next() {
		w := e.Value.(IBeanDefinition)
		path += fmt.Sprintf("=> %s ↩\n", w.Description())
	}
	return path[:len(path)-1]
}

// beanAssembly Bean 组装车间
type beanAssembly interface {
	SpringContext() SpringContext
	collectBeans(v reflect.Value) bool
	getBeanValue(v reflect.Value, beanId string, parentValue reflect.Value, field string) bool
}

// defaultBeanAssembly beanAssembly 的默认版本
type defaultBeanAssembly struct {
	springContext SpringContext
	beanCache     map[reflect.Type]*beanCacheItem
	wiringStack   *wiringStack
}

// newDefaultBeanAssembly defaultBeanAssembly 的构造函数
func newDefaultBeanAssembly(springContext SpringContext,
	beanCache map[reflect.Type]*beanCacheItem,
	watcher []WiringWatcher) *defaultBeanAssembly {

	return &defaultBeanAssembly{
		springContext: springContext,
		beanCache:     beanCache,
		wiringStack:   newWiringStack(watcher),
	}
}

func (beanAssembly *defaultBeanAssembly) SpringContext() SpringContext {
	return beanAssembly.springContext
}

// getCacheItem 获取指定类型的缓存项，返回值不会为 nil。
func (beanAssembly *defaultBeanAssembly) getCacheItem(t reflect.Type) *beanCacheItem {
	if c, ok := beanAssembly.beanCache[t]; ok {
		return c
	}
	return newBeanCacheItem()
}

// getBeanValue 根据 BeanId 查找 Bean 并返回 Bean 源的值
func (beanAssembly *defaultBeanAssembly) getBeanValue(v reflect.Value, beanId string, parentValue reflect.Value, field string) bool {

	typeName, beanName, nullable := ParseBeanId(beanId)
	beanType := v.Type()

	if ok := IsRefType(beanType.Kind()); !ok {
		panic(fmt.Errorf("receiver must be ref type, bean: \"%s\" field: %s", beanId, field))
	}

	result := make([]*BeanDefinition, 0)

	m := beanAssembly.getCacheItem(beanType)
	for _, bean := range m.beans {
		// 不能将自身赋给自身的字段 && 类型必须相容 && 类型全限定名匹配
		if bean.Value() != parentValue && bean.Type().AssignableTo(beanType) && bean.Match(typeName, beanName) {
			result = append(result, bean)
		}
	}

	count := len(result)

	// 没有找到
	if count == 0 {
		if nullable {
			return false
		} else {
			panic(fmt.Errorf("can't find bean, bean: \"%s\" field: %s type: %s", beanId, field, beanType))
		}
	}

	var primaryBeans []*BeanDefinition

	for _, bean := range result {
		if bean.primary {
			primaryBeans = append(primaryBeans, bean)
		}
	}

	if len(primaryBeans) > 1 {
		msg := fmt.Sprintf("found %d primary beans, bean: \"%s\" field: %s type: %s [", len(primaryBeans), beanId, field, beanType)
		for _, b := range primaryBeans {
			msg += "( " + b.Description() + " ), "
		}
		msg = msg[:len(msg)-2] + "]"
		panic(errors.New(msg))
	}

	if len(primaryBeans) == 0 {
		if count > 1 {
			msg := fmt.Sprintf("found %d beans, bean: \"%s\" field: %s type: %s [", len(result), beanId, field, beanType)
			for _, b := range result {
				msg += "( " + b.Description() + " ), "
			}
			msg = msg[:len(msg)-2] + "]"
			panic(errors.New(msg))
		}
		primaryBeans = append(primaryBeans, result[0])
	}

	// 依赖注入
	beanAssembly.wireBeanDefinition(primaryBeans[0], false)

	// 恰好 1 个
	v0 := SpringUtils.ValuePatchIf(v, beanAssembly.springContext.AllAccess())
	v0.Set(primaryBeans[0].Value())
	return true
}

// collectBeans 收集符合条件的 Bean 源
func (beanAssembly *defaultBeanAssembly) collectBeans(v reflect.Value) bool {

	t := v.Type()
	ev := reflect.New(t).Elem()

	// 查找数组类型
	{
		m := beanAssembly.getCacheItem(t)
		for _, d := range m.beans {

			// 数组扩容，提高内存分配性能
			size := ev.Len() + d.Value().Len()
			newSlice := reflect.MakeSlice(t, size, size)

			reflect.Copy(newSlice, ev)

			// 拷贝新元素
			for i := 0; i < d.Value().Len(); i++ {
				di := d.Value().Index(i)

				if di.Kind() == reflect.Struct {
					bd := ValueToBeanDefinition("", di.Addr())
					bd.file = d.getFile()
					bd.line = d.getLine()
					beanAssembly.wireBeanDefinition(bd, false)

				} else if di.Kind() == reflect.Ptr {
					if de := di.Elem(); de.Kind() == reflect.Struct {
						bd := ValueToBeanDefinition("", di)
						bd.file = d.getFile()
						bd.line = d.getLine()
						beanAssembly.wireBeanDefinition(bd, false)
					}
				}

				newSlice.Index(i + ev.Len()).Set(di)
			}

			// 完成扩容
			ev = newSlice
		}
	}

	// 查找单例类型
	{
		et := t.Elem()
		m := beanAssembly.getCacheItem(et)
		for _, d := range m.beans {
			beanAssembly.wireBeanDefinition(d, false)
			ev = reflect.Append(ev, d.Value())
		}
	}

	if ev.Len() > 0 {
		v = SpringUtils.ValuePatchIf(v, beanAssembly.springContext.AllAccess())
		v.Set(ev)
		return true
	}
	return false
}

// fieldBeanDefinition 带字段名称的 BeanDefinition 实现
type fieldBeanDefinition struct {
	*BeanDefinition
	field string // 字段名称
}

// Description 返回 Bean 的详细描述
func (d *fieldBeanDefinition) Description() string {
	return fmt.Sprintf("%s field: %s %s", d.bean.beanClass(), d.field, d.Caller())
}

// delegateBeanDefinition 代理功能的 BeanDefinition 实现
type delegateBeanDefinition struct {
	*BeanDefinition
	delegate IBeanDefinition // 代理项
}

// Description 返回 Bean 的详细描述
func (d *delegateBeanDefinition) Description() string {
	return fmt.Sprintf("%s value %s", d.delegate.springBean().beanClass(), d.delegate.Caller())
}

// wireBeanDefinition 绑定 BeanDefinition 指定的 Bean
func (beanAssembly *defaultBeanAssembly) wireBeanDefinition(bd IBeanDefinition, onlyAutoWire bool) {

	// 是否已删除
	if bd.getStatus() == beanStatus_Deleted {
		panic(fmt.Errorf("bean: \"%s\" have been deleted", bd.BeanId()))
	}

	// 是否已绑定
	if bd.getStatus() == beanStatus_Wired {
		return
	}

	// 将当前 Bean 放入注入栈，以便检测循环依赖。
	beanAssembly.wiringStack.pushBack(bd)

	// 是否循环依赖
	if bd.getStatus() == beanStatus_Wiring {
		if _, ok := bd.springBean().(*objectBean); !ok {
			panic(errors.New("found circle autowire"))
		}
		return
	}

	bd.setStatus(beanStatus_Wiring)

	// 首先初始化当前 Bean 不直接依赖的那些 Bean
	for _, selector := range bd.getDependsOn() {
		if bean, ok := beanAssembly.springContext.FindBean(selector); !ok {
			panic(fmt.Errorf("can't find bean: \"%v\"", selector))
		} else {
			beanAssembly.wireBeanDefinition(bean, false)
		}
	}

	// 如果是成员方法 Bean，需要首先初始化它的父 Bean
	if mBean, ok := bd.springBean().(*methodBean); ok {
		beanAssembly.wireBeanDefinition(mBean.parent, false)
	}

	switch bean := bd.springBean().(type) {
	case *objectBean: // 原始对象
		beanAssembly.wireObjectBean(bd, onlyAutoWire)
	case *constructorBean: // 构造函数
		fnValue := reflect.ValueOf(bean.fn)
		beanAssembly.wireFunctionBean(fnValue, &bean.functionBean, bd)
	case *methodBean: // 成员方法
		fnValue := bean.parent.Value().MethodByName(bean.method)
		beanAssembly.wireFunctionBean(fnValue, &bean.functionBean, bd)
	default:
		panic(errors.New("unknown spring bean type"))
	}

	// 如果有则执行用户设置的初始化函数
	if bd.getInit() != nil {
		fnValue := reflect.ValueOf(bd.getInit())
		fnValue.Call([]reflect.Value{bd.Value()})
	}

	// 删除保存的注入帧
	beanAssembly.wiringStack.popBack()

	bd.setStatus(beanStatus_Wired)
}

// wireObjectBean 对原始对象进行注入
func (beanAssembly *defaultBeanAssembly) wireObjectBean(bd IBeanDefinition, onlyAutoWire bool) {

	st := bd.Type()
	sk := st.Kind()

	if sk == reflect.Slice { // 处理数组 Bean
		et := st.Elem()
		ek := et.Kind()

		if ek == reflect.Struct { // 结构体数组
			v := bd.Value()
			for i := 0; i < v.Len(); i++ {
				iv := v.Index(i).Addr()
				b := ValueToBeanDefinition("", iv)
				b.file = bd.getFile()
				b.line = bd.getLine()
				beanAssembly.wireBeanDefinition(b, false)
			}

		} else if ek == reflect.Ptr { // 指针数组
			it := et.Elem()
			ik := it.Kind()

			if ik == reflect.Struct { // 结构体指针数组
				v := bd.Value()
				for p := 0; p < v.Len(); p++ {
					pv := v.Index(p)
					b := ValueToBeanDefinition("", pv)
					b.file = bd.getFile()
					b.line = bd.getLine()
					beanAssembly.wireBeanDefinition(b, false)
				}
			}
		}

	} else if sk == reflect.Ptr { // 处理指针 Bean
		et := st.Elem()
		if et.Kind() == reflect.Struct { // 结构体指针

			var etName string
			if etName = et.Name(); etName == "" {
				etName = et.String()
			}

			sv := bd.Value()
			ev := sv.Elem()

			for i := 0; i < et.NumField(); i++ {
				// 字段包含 value 标签时嵌套处理只注入变量
				fieldOnlyAutoWire := false

				ft := et.Field(i)
				fv := ev.Field(i)

				fieldName := etName + ".$" + ft.Name

				// 处理 value 标签
				if !onlyAutoWire {
					// 避免父结构体有 value 标签重新解析导致失败的情况
					if tag, ok := ft.Tag.Lookup("value"); ok {
						fieldOnlyAutoWire = true
						bindStructField(beanAssembly.springContext, ft.Type, fv, fieldName, "", tag, beanAssembly.springContext.AllAccess(), "")
					}
				}

				// 处理 autowire 标签
				if beanId, ok := ft.Tag.Lookup("autowire"); ok {
					beanAssembly.wireStructField(sv, fv, fieldName, beanId)
				}

				// 处理结构体类型的字段，防止递归所以不支持指针结构体字段
				if ft.Type.Kind() == reflect.Struct {
					// 开放私有字段，但是不会更新原属性
					fv0 := SpringUtils.ValuePatchIf(fv, beanAssembly.springContext.AllAccess())
					if fv0.CanSet() {

						b := ValueToBeanDefinition("", fv0.Addr())
						b.file = bd.getFile()
						b.line = bd.getLine()
						fbd := &fieldBeanDefinition{b, fieldName}
						beanAssembly.wireBeanDefinition(fbd, fieldOnlyAutoWire)
					}
				}
			}
		}
	}
}

// wireFunctionBean 对函数定义 Bean 进行注入
func (beanAssembly *defaultBeanAssembly) wireFunctionBean(fnValue reflect.Value, bean *functionBean, bd IBeanDefinition) {

	in := bean.arg.Get(beanAssembly, bd)
	out := fnValue.Call(in)

	// 获取第一个返回值
	val := out[0]

	// 检查是否有 error 返回
	if len(out) == 2 {
		if err := out[1].Interface(); err != nil {
			panic(fmt.Errorf("function bean: \"%s\" return error: %v", bd.Caller(), err))
		}
	}

	if IsRefType(val.Kind()) {
		// 如果实现接口的值是个结构体，那么需要转换成指针类型然后赋给接口
		if val.Kind() == reflect.Interface && val.Elem().Kind() == reflect.Struct {
			ptrVal := reflect.New(val.Elem().Type())
			ptrVal.Elem().Set(val.Elem())
			bean.rValue.Set(ptrVal)
		} else {
			bean.rValue.Set(val)
		}
	} else {
		bean.rValue.Elem().Set(val)
	}

	if bean.bean = bean.rValue.Interface(); bean.bean == nil {
		panic(fmt.Errorf("function bean: \"%s\" return nil", bd.Caller()))
	}

	// 对返回值进行依赖注入
	b := &BeanDefinition{
		name:   bd.Name(),
		status: beanStatus_Default,
		file:   bd.getFile(),
		line:   bd.getLine(),
	}

	if bean.Type().Kind() == reflect.Interface {
		b.bean = newObjectBean(bean.Value().Elem())
	} else {
		b.bean = newObjectBean(bean.Value())
	}

	beanAssembly.wireBeanDefinition(&delegateBeanDefinition{b, bd}, false)
}

// wireStructField 对结构体的字段进行绑定
func (beanAssembly *defaultBeanAssembly) wireStructField(parentValue reflect.Value,
	beanValue reflect.Value, field string, beanId string) {

	_, beanName, nullable := ParseBeanId(beanId)
	if beanName == "[]" { // 收集模式，autowire:"[]"

		// 收集模式的绑定对象必须是数组
		if beanValue.Type().Kind() != reflect.Slice {
			panic(fmt.Errorf("field: %s should be slice", field))
		}

		ok := beanAssembly.collectBeans(beanValue)
		if !ok && !nullable { // 没找到且不能为空则 panic
			panic(fmt.Errorf("can't find bean: \"%s\" field: %s", beanId, field))
		}

	} else { // 匹配模式，autowire:"" or autowire:"name"
		beanAssembly.getBeanValue(beanValue, beanId, parentValue, field)
	}
}

// defaultSpringContext SpringContext 的默认版本
type defaultSpringContext struct {
	// 属性值列表接口
	Properties

	// 上下文接口
	context.Context
	cancel context.CancelFunc

	profile   string // 运行环境
	autoWired bool   // 已经开始自动绑定
	allAccess bool   // 允许注入私有字段

	eventNotify func(event ContextEvent) // 事件通知函数

	beanMap     map[beanKey]*BeanDefinition     // Bean 的集合
	beanCache   map[reflect.Type]*beanCacheItem // Bean 的缓存
	methodBeans []*BeanDefinition               // 方法 Beans

	Sort bool // 自动注入期间是否按照 BeanId 进行排序并依次进行注入
}

// NewDefaultSpringContext defaultSpringContext 的构造函数
func NewDefaultSpringContext() *defaultSpringContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &defaultSpringContext{
		Context:     ctx,
		cancel:      cancel,
		Properties:  NewDefaultProperties(),
		methodBeans: make([]*BeanDefinition, 0),
		beanMap:     make(map[beanKey]*BeanDefinition),
		beanCache:   make(map[reflect.Type]*beanCacheItem),
	}
}

// GetProfile 返回运行环境
func (ctx *defaultSpringContext) GetProfile() string {
	return ctx.profile
}

// SetProfile 设置运行环境
func (ctx *defaultSpringContext) SetProfile(profile string) {
	ctx.profile = profile
}

// AllAccess 返回是否允许访问私有字段
func (ctx *defaultSpringContext) AllAccess() bool {
	return ctx.allAccess
}

// SetAllAccess 设置是否允许访问私有字段
func (ctx *defaultSpringContext) SetAllAccess(allAccess bool) {
	ctx.allAccess = allAccess
}

// SetEventNotify 设置 Context 事件通知函数
func (ctx *defaultSpringContext) SetEventNotify(notify func(event ContextEvent)) {
	ctx.eventNotify = notify
}

// checkAutoWired 检查是否已调用 AutoWireBeans 方法
func (ctx *defaultSpringContext) checkAutoWired() {
	if !ctx.autoWired {
		panic(errors.New("should call after ctx.AutoWireBeans()"))
	}
}

// checkRegistration 检查注册是否已被冻结
func (ctx *defaultSpringContext) checkRegistration() {
	if ctx.autoWired {
		panic(errors.New("bean registration have been frozen"))
	}
}

// deleteBeanDefinition 删除 BeanDefinition。
func (ctx *defaultSpringContext) deleteBeanDefinition(bd *BeanDefinition) {
	key := beanKey{bd.Type(), bd.name}
	bd.status = beanStatus_Deleted
	delete(ctx.beanMap, key)
}

// registerBeanDefinition 注册 BeanDefinition，重复注册会 panic。
func (ctx *defaultSpringContext) registerBeanDefinition(d *BeanDefinition) {
	ctx.checkRegistration()

	k := beanKey{
		rType: d.Type(),
		name:  d.name,
	}

	if _, ok := ctx.beanMap[k]; ok {
		panic(fmt.Errorf("duplicate registration, bean: \"%s\"", d.BeanId()))
	}

	ctx.beanMap[k] = d
}

// RegisterBean 注册单例 Bean，不指定名称，重复注册会 panic。
func (ctx *defaultSpringContext) RegisterBean(bean interface{}) *BeanDefinition {
	return ctx.RegisterNameBean("", bean)
}

// RegisterNameBean 注册单例 Bean，需要指定名称，重复注册会 panic。
func (ctx *defaultSpringContext) RegisterNameBean(name string, bean interface{}) *BeanDefinition {
	bd := ToBeanDefinition(name, bean)
	ctx.registerBeanDefinition(bd)
	return bd
}

// RegisterBeanFn 注册单例构造函数 Bean，不指定名称，重复注册会 panic。
func (ctx *defaultSpringContext) RegisterBeanFn(fn interface{}, tags ...string) *BeanDefinition {
	return ctx.RegisterNameBeanFn("", fn, tags...)
}

// RegisterNameBeanFn 注册单例构造函数 Bean，需指定名称，重复注册会 panic。
func (ctx *defaultSpringContext) RegisterNameBeanFn(name string, fn interface{}, tags ...string) *BeanDefinition {
	bd := FnToBeanDefinition(name, fn, tags...)
	ctx.registerBeanDefinition(bd)
	return bd
}

// RegisterMethodBean 注册成员方法单例 Bean，不指定名称，重复注册会 panic。
// selector 可以是 *BeanDefinition，可以是 BeanId，还可以是 (Type)(nil) 变量。
// 必须给定方法名而不能通过遍历方法列表比较方法类型的方式获得函数名，因为不同方法的类型可能相同。
// 而且 interface 的方法类型不带 receiver 而成员方法的类型带有 receiver，两者类型不好匹配。
func (ctx *defaultSpringContext) RegisterMethodBean(selector interface{}, method string, tags ...string) *BeanDefinition {
	return ctx.RegisterNameMethodBean("", selector, method, tags...)
}

// RegisterNameMethodBean 注册成员方法单例 Bean，需指定名称，重复注册会 panic。
// selector 可以是 *BeanDefinition，可以是 BeanId，还可以是 (Type)(nil) 变量。
// 必须给定方法名而不能通过遍历方法列表比较方法类型的方式获得函数名，因为不同方法的类型可能相同。
// 而且 interface 的方法类型不带 receiver 而成员方法的类型带有 receiver，两者类型不好匹配。
func (ctx *defaultSpringContext) RegisterNameMethodBean(name string, selector interface{}, method string, tags ...string) *BeanDefinition {
	ctx.checkRegistration()

	if selector == nil || selector == "" {
		panic(errors.New("selector can't be nil or empty"))
	}

	bd := MethodToBeanDefinition(name, selector, method, tags...)
	ctx.methodBeans = append(ctx.methodBeans, bd)
	return bd
}

// GetBean 根据类型获取单例 Bean，若多于 1 个则 panic；找到返回 true 否则返回 false。
func (ctx *defaultSpringContext) GetBean(i interface{}, watcher ...WiringWatcher) bool {
	return ctx.GetBeanByName("?", i, watcher...)
}

// GetBeanByName 根据名称和类型获取单例 Bean，若多于 1 个则 panic；找到返回 true 否则返回 false。
func (ctx *defaultSpringContext) GetBeanByName(beanId string, i interface{}, watcher ...WiringWatcher) bool {
	ctx.checkAutoWired()

	// 确保存在可空标记，抑制 panic 效果。
	if beanId == "" || beanId[len(beanId)-1] != '?' {
		beanId += "?"
	}

	t := reflect.TypeOf(i)

	// 使用指针才能够对外赋值
	if t.Kind() != reflect.Ptr {
		panic(errors.New("i must be pointer"))
	}

	v := reflect.ValueOf(i)

	w := newDefaultBeanAssembly(ctx, ctx.beanCache, watcher)
	return w.getBeanValue(v.Elem(), beanId, reflect.Value{}, "")
}

// FindBean 获取单例 Bean，若多于 1 个则 panic；找到返回 true 否则返回 false。
// selector 可以是 BeanId，还可以是 (Type)(nil) 变量，Type 为接口类型时带指针。
func (ctx *defaultSpringContext) FindBean(selector interface{}) (*BeanDefinition, bool) {
	ctx.checkAutoWired()

	finder := func(fn func(*BeanDefinition) bool) (result []*BeanDefinition) {
		for _, bean := range ctx.beanMap {
			if fn(bean) {

				// 如果 Bean 正在解析则跳过
				if bean.status == beanStatus_Resolving {
					continue
				}

				// 避免 Bean 还未解析
				ctx.resolveBean(bean)

				if bean.status != beanStatus_Deleted {
					result = append(result, bean)
				}
			}
		}
		return
	}

	var result []*BeanDefinition

	switch o := selector.(type) {
	case string:
		typeName, beanName, _ := ParseBeanId(o)
		result = finder(func(b *BeanDefinition) bool {
			return b.Match(typeName, beanName)
		})
	default:
		{
			t := reflect.TypeOf(o) // map、slice 等不是指针类型
			if t.Kind() == reflect.Ptr {
				e := t.Elem()
				if e.Kind() == reflect.Interface {
					t = e // 接口类型去掉指针
				}
			}

			result = finder(func(b *BeanDefinition) bool {
				return b.Type().AssignableTo(t)
			})
		}
	}

	count := len(result)

	// 没有找到
	if count == 0 {
		return nil, false
	}

	// 多于 1 个
	if count > 1 {
		msg := fmt.Sprintf("found %d beans, bean: \"%v\" [", len(result), selector)
		for _, b := range result {
			msg += "( " + b.Description() + " ), "
		}
		msg = msg[:len(msg)-2] + "]"
		panic(errors.New(msg))
	}

	// 恰好 1 个 & 仅供查询无需绑定
	return result[0], true
}

// FindBeanByName 根据名称和类型获取单例 Bean，若多于 1 个则 panic；找到返回 true 否则返回 false。
func (ctx *defaultSpringContext) FindBeanByName(beanId string) (*BeanDefinition, bool) {
	return ctx.FindBean(beanId)
}

// CollectBeans 收集数组或指针定义的所有符合条件的 Bean 对象，收集到返回 true，否则返回 false。
func (ctx *defaultSpringContext) CollectBeans(i interface{}, watcher ...WiringWatcher) bool {
	ctx.checkAutoWired()

	t := reflect.TypeOf(i)

	if t.Kind() != reflect.Ptr {
		panic(errors.New("i must be slice ptr"))
	}

	et := t.Elem()

	if et.Kind() != reflect.Slice {
		panic(errors.New("i must be slice ptr"))
	}

	w := newDefaultBeanAssembly(ctx, ctx.beanCache, watcher)
	return w.collectBeans(reflect.ValueOf(i).Elem())
}

// findCacheItem 查找指定类型的缓存项
func (ctx *defaultSpringContext) findCacheItem(t reflect.Type) *beanCacheItem {
	c, ok := ctx.beanCache[t]
	if !ok {
		c = newBeanCacheItem()
		ctx.beanCache[t] = c
	}
	return c
}

// resolveBean 对 Bean 进行决议是否能够创建 Bean 的实例
func (ctx *defaultSpringContext) resolveBean(bd *BeanDefinition) {

	if bd.status > beanStatus_Default {
		return
	}

	bd.status = beanStatus_Resolving

	// 如果是成员方法 Bean，需要首先决议它的父 Bean 是否能实例化
	if mBean, ok := bd.bean.(*methodBean); ok {
		ctx.resolveBean(mBean.parent)

		// 父 Bean 已经被删除了，子 Bean 也不应该存在
		if mBean.parent.status == beanStatus_Deleted {
			ctx.deleteBeanDefinition(bd)
			return
		}
	}

	if ok := bd.Matches(ctx); !ok { // 不满足则删除注册
		ctx.deleteBeanDefinition(bd)
		return
	}

	// 将符合注册条件的 Bean 放入到缓存里面
	item := ctx.findCacheItem(bd.Type())
	item.store(bd.Type(), bd)

	// 按照导出类型放入缓存
	for _, t := range bd.exports {

		// 检查是否实现了导出接口
		if ok := bd.Type().Implements(t); !ok {
			panic(fmt.Errorf("%s not implement %s interface", bd.Description(), t.String()))
		}

		m := ctx.findCacheItem(t)
		m.store(t, bd)
	}

	bd.status = beanStatus_Resolved
}

// AutoWireBeans 完成自动绑定
func (ctx *defaultSpringContext) AutoWireBeans(watcher ...WiringWatcher) {

	// 不再接受 Bean 注册，因为性能的原因使用了缓存，并且在 AutoWireBeans 的过程中
	// 逐步建立起这个缓存，而随着缓存的建立，绑定的速度会越来越快，从而减少性能的损失。

	if ctx.autoWired {
		panic(errors.New("ctx.AutoWireBeans() already called"))
	}

	// 注册所有的 Method Bean
	for _, bd := range ctx.methodBeans {

		var (
			selector string
			filter   func(*BeanDefinition) bool
		)

		bean := bd.bean.(*fakeMethodBean)
		result := make([]*BeanDefinition, 0)

		switch e := bean.selector.(type) {
		case *BeanDefinition:
			selector = e.BeanId()
			result = append(result, e)
		case string:
			selector = e
			typeName, beanName, _ := ParseBeanId(e)
			filter = func(b *BeanDefinition) bool {
				return b.Match(typeName, beanName)
			}
		default:
			t := reflect.TypeOf(e)
			// 如果是接口类型需要解除外层指针
			if t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Interface {
				t = t.Elem()
			}
			selector = t.String()
			filter = func(b *BeanDefinition) bool {
				return b.Type() == t
			}
		}

		if filter != nil {
			for _, b := range ctx.beanMap {
				if filter(b) {
					result = append(result, b)
				}
			}
		}

		if l := len(result); l == 0 {
			panic(fmt.Errorf("can't find parent bean: \"%s\"", selector))
		} else if l > 1 {
			panic(fmt.Errorf("found %d parent bean: \"%s\"", l, selector))
		}

		bd.bean = newMethodBean(result[0], bean.method, bean.tags...)
		if bd.name == "" { // 使用默认名称
			bd.name = bd.bean.Type().String()
		}
		ctx.registerBeanDefinition(bd)
	}

	ctx.autoWired = true

	if ctx.eventNotify != nil {
		ctx.eventNotify(ContextEvent_ResolveStart)
	}

	// 首先决议 Bean 是否能够注册，否则会删除其注册信息
	for _, bd := range ctx.beanMap {
		ctx.resolveBean(bd)
	}

	if ctx.eventNotify != nil {
		ctx.eventNotify(ContextEvent_ResolveEnd)
	}

	w := newDefaultBeanAssembly(ctx, ctx.beanCache, watcher)

	if ctx.eventNotify != nil {
		ctx.eventNotify(ContextEvent_AutoWireStart)
	}

	defer func() { // 捕获自动注入过程中的异常，打印错误日志然后重新抛出
		if err := recover(); err != nil {
			SpringLogger.Errorf("%v ↩\n%s", err, w.wiringStack.path())
			panic(err)
		}
	}()

	if ctx.Sort { // 自动注入期间是否排序注入
		beanKeyMap := map[string]beanKey{}
		for key, val := range ctx.beanMap {
			beanKeyMap[val.BeanId()] = key
		}

		beanIds := make([]string, 0)
		for s, _ := range beanKeyMap {
			beanIds = append(beanIds, s)
		}

		sort.Strings(beanIds)

		for _, beanId := range beanIds {
			key := beanKeyMap[beanId]
			bd := ctx.beanMap[key]
			w.wireBeanDefinition(bd, false)
		}

	} else {
		for _, bd := range ctx.beanMap {
			w.wireBeanDefinition(bd, false)
		}
	}

	if ctx.eventNotify != nil {
		ctx.eventNotify(ContextEvent_AutoWireEnd)
	}
}

// WireBean 绑定外部的 Bean 源
func (ctx *defaultSpringContext) WireBean(bean interface{}, watcher ...WiringWatcher) {
	ctx.checkAutoWired()

	w := newDefaultBeanAssembly(ctx, ctx.beanCache, watcher)
	bd := ToBeanDefinition("", bean)
	w.wireBeanDefinition(bd, false)
}

// GetBeanDefinitions 获取所有 Bean 的定义，一般仅供调试使用。
func (ctx *defaultSpringContext) GetBeanDefinitions() []*BeanDefinition {
	result := make([]*BeanDefinition, 0)
	for _, v := range ctx.beanMap {
		result = append(result, v)
	}
	return result
}

// Close 关闭容器上下文，用于通知 Bean 销毁等。
func (ctx *defaultSpringContext) Close() {

	if ctx.eventNotify != nil {
		ctx.eventNotify(ContextEvent_CloseStart)
	}

	// 执行销毁函数
	for _, bd := range ctx.beanMap {
		if bd.destroy != nil {
			fnValue := reflect.ValueOf(bd.destroy)
			fnValue.Call([]reflect.Value{bd.Value()})
		}
	}

	// 上下文结束
	ctx.cancel()

	if ctx.eventNotify != nil {
		ctx.eventNotify(ContextEvent_CloseEnd)
	}
}
