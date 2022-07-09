package cl

import (
	"go/constant"
	"go/token"
	"go/types"
	"log"
	"strconv"

	ctypes "github.com/weblfe/c2go/clang/types"

	"github.com/goplus/gox"
	"github.com/weblfe/c2go/clang/ast"
	"github.com/weblfe/c2go/clang/types/parser"
)

// -----------------------------------------------------------------------------

func toType(ctx *blockCtx, typ *ast.Type, flags int) types.Type {
	t, _ := toTypeEx(ctx, ctx.cb.Scope(), nil, typ, flags)
	return t
}

func toTypeEx(ctx *blockCtx, scope *types.Scope, tyAnonym types.Type, typ *ast.Type, flags int) (t types.Type, kind int) {
	conf := &parser.Config{
		Pkg: ctx.pkg.Types, Scope: scope, Flags: flags,
		TyAnonym: tyAnonym, TyInt128: ctx.tyI128, TyUint128: ctx.tyU128,
	}
retry:
	t, kind, err := parser.ParseType(typ.QualType, conf)
	if err != nil {
		if e, ok := err.(*parser.TypeNotFound); ok && e.StructOrUnion {
			ctx.typdecls[e.Literal] = ctx.cb.NewType(e.Literal)
			goto retry
		}
		log.Panicln("toType:", err, "-", typ.QualType)
	}
	return
}

func toStructType(ctx *blockCtx, t *types.Named, struc *ast.Node) (ret *types.Struct, dels delfunc) {
	b := newStructBuilder()
	scope := types.NewScope(ctx.cb.Scope(), token.NoPos, token.NoPos, "")
	n := len(struc.Inner)
	for i := 0; i < n; i++ {
		decl := struc.Inner[i]
		switch decl.Kind {
		case ast.FieldDecl:
			if debugCompileDecl {
				log.Println("  => field", decl.Name, "-", decl.Type.QualType)
			}
			avoidKeyword(&decl.Name)
			typ, _ := toTypeEx(ctx, scope, nil, decl.Type, parser.FlagIsField)
			if decl.IsBitfield {
				bits := toInt64(ctx, decl.Inner[0], "non-constant bit field")
				b.BitField(ctx, typ, decl.Name, int(bits))
			} else {
				b.Field(ctx, ctx.goNodePos(decl), typ, decl.Name, false)
			}
		case ast.RecordDecl:
			name, suKind := ctx.getSuName(decl, decl.TagUsed)
			typ, del := compileStructOrUnion(ctx, name, decl)
			if suKind != suAnonymous {
				break
			}
			dels = append(dels, name)
			dels = append(dels, del...)
			for i+1 < n {
				next := struc.Inner[i+1]
				if next.Kind == ast.FieldDecl {
					if next.IsImplicit {
						b.Field(ctx, ctx.goNodePos(decl), typ, name, true)
						i++
					} else if ret, ok := checkAnonymous(ctx, scope, typ, next); ok {
						b.Field(ctx, ctx.goNodePos(next), ret, next.Name, false)
						i++
						continue
					}
				}
				break
			}
		case ast.IndirectFieldDecl, ast.MaxFieldAlignmentAttr, ast.AlignedAttr, ast.PackedAttr:
		default:
			log.Panicln("toStructType: unknown field kind =", decl.Kind)
		}
	}
	ret = b.Type(ctx, t)
	return
}

func toUnionType(ctx *blockCtx, t *types.Named, unio *ast.Node) (ret types.Type, dels delfunc) {
	b := newUnionBuilder()
	scope := types.NewScope(ctx.cb.Scope(), token.NoPos, token.NoPos, "")
	n := len(unio.Inner)
	for i := 0; i < n; i++ {
		decl := unio.Inner[i]
		switch decl.Kind {
		case ast.FieldDecl:
			if debugCompileDecl {
				log.Println("  => field", decl.Name, "-", decl.Type.QualType)
			}
			typ, _ := toTypeEx(ctx, scope, nil, decl.Type, 0)
			b.Field(ctx, ctx.goNodePos(decl), typ, decl.Name, false)
		case ast.RecordDecl:
			name, suKind := ctx.getSuName(decl, decl.TagUsed)
			typ, del := compileStructOrUnion(ctx, name, decl)
			if suKind != suAnonymous {
				break
			}
			dels = append(dels, name)
			dels = append(dels, del...)
			for i+1 < n {
				next := unio.Inner[i+1]
				if next.Kind == ast.FieldDecl {
					if next.IsImplicit {
						b.Field(ctx, ctx.goNodePos(decl), typ, name, true)
						i++
					} else if ret, ok := checkAnonymous(ctx, scope, typ, next); ok {
						b.Field(ctx, ctx.goNodePos(next), ret, next.Name, false)
						i++
						continue
					}
				}
				break
			}
		case ast.IndirectFieldDecl, ast.MaxFieldAlignmentAttr, ast.AlignedAttr, ast.PackedAttr:
		default:
			log.Panicln("toUnionType: unknown field kind =", decl.Kind)
		}
	}
	ret = b.Type(ctx, t)
	return
}

func checkAnonymous(ctx *blockCtx, scope *types.Scope, typ types.Type, v *ast.Node) (ret types.Type, ok bool) {
	ret, kind := toTypeEx(ctx, scope, typ, v.Type, 0)
	ok = (kind & parser.KindFAnonymous) != 0
	return
}

// -----------------------------------------------------------------------------

func compileTypedef(ctx *blockCtx, decl *ast.Node, global bool) types.Type {
	name, qualType := decl.Name, decl.Type.QualType
	if debugCompileDecl {
		log.Println("typedef", name, "-", qualType)
	}
	if global && ctx.checkExists(name) {
		if len(decl.Inner) > 0 { // check to delete unnamed types
			item := decl.Inner[0]
			if item.Kind == "ElaboratedType" {
				if owned := item.OwnedTagDecl; owned != nil && owned.Name == "" && owned.Kind != ast.EnumDecl {
					ctx.deleteUnnamed(owned.ID)
				}
			}
		}
		return nil
	}
	scope := ctx.cb.Scope()
	if len(decl.Inner) > 0 {
		item := decl.Inner[0]
		if item.Kind == "ElaboratedType" {
			if owned := item.OwnedTagDecl; owned != nil && owned.Name == "" {
				var typ types.Type
				if owned.Kind == ast.EnumDecl {
					typ = ctypes.Int
				} else if u, ok := ctx.unnameds[owned.ID]; ok {
					typ = u.typ
				} else {
					log.Panicf("compileTypedef %v: unknown id = %v\n", name, owned.ID)
				}
				ctx.cb.AliasType(name, typ, ctx.goNodePos(decl))
				return typ
			}
		}
	}
	typ := toType(ctx, decl.Type, parser.FlagIsTypedef)
	if isArrayUnknownLen(typ) || typ == ctypes.Void {
		aliasType(scope, ctx.pkg.Types, name, typ)
		return nil
	}
	if global {
		if old := scope.Lookup(name); old != nil {
			if types.Identical(typ, old.Type()) {
				return nil
			}
		}
	}
	ctx.cb.AliasType(name, typ, ctx.goNodePos(decl))
	return typ
}

func compileStructOrUnion(ctx *blockCtx, name string, decl *ast.Node) (*types.Named, delfunc) {
	if debugCompileDecl {
		log.Println(decl.TagUsed, name, "-", decl.Loc.PresumedLine)
	}
	t, decled := ctx.typdecls[name]
	if !decled {
		t = ctx.cb.NewType(name, ctx.goNodePos(decl))
		ctx.typdecls[name] = t
	}
	if decl.CompleteDefinition {
		var inner types.Type
		var del delfunc
		switch decl.TagUsed {
		case "struct":
			inner, del = toStructType(ctx, t.Type(), decl)
		default:
			inner, del = toUnionType(ctx, t.Type(), decl)
		}
		return t.InitType(ctx.pkg, inner), del
	}
	return t.Type(), nil
}

func compileEnum(ctx *blockCtx, decl *ast.Node, global bool) {
	inner := decl.Inner
	if global && len(inner) > 0 && ctx.checkExists(inner[0].Name) {
		return
	}
	scope := ctx.cb.Scope()
	cdecl := ctx.pkg.NewConstDefs(scope)
	iotav := 0
	for _, item := range inner {
		iotav = compileEnumConst(ctx, cdecl, item, iotav)
	}
}

func compileEnumConst(ctx *blockCtx, cdecl *gox.ConstDefs, v *ast.Node, iotav int) int {
	fn := func(cb *gox.CodeBuilder) int {
		if len(v.Inner) > 0 {
			compileExpr(ctx, v.Inner[0])
			cval := cb.Get(-1).CVal
			if cval == nil {
				log.Panicln("compileEnumConst: not a constant expression")
			}
			ival, ok := constant.Int64Val(cval)
			if !ok {
				log.Panicln("compileEnumConst: not a integer constant")
			}
			iotav = int(ival)
		} else {
			cb.Val(iotav)
		}
		return 1
	}
	cdecl.New(fn, iotav, ctx.goNodePos(v), ctypes.Int, v.Name)
	return iotav + 1
}

func compileVarDecl(ctx *blockCtx, decl *ast.Node, global bool) {
	if debugCompileDecl {
		log.Println("varDecl", decl.Name, "-", decl.Loc.PresumedLine)
	}
	if global {
		ctx.getPubName(&decl.Name)
	}
	scope := ctx.cb.Scope()
	flags := 0
	static := ""
	switch decl.StorageClass {
	case ast.Extern:
		flags = parser.FlagIsExtern
	case ast.Static:
		if !global { // local static variable
			scope = ctx.pkg.Types.Scope()
		}
		static = decl.Name
		decl.Name = ctx.autoStaticName(static)
	}
	typ, kind := toTypeEx(ctx, scope, nil, decl.Type, flags)
	avoidKeyword(&decl.Name)
	if flags == parser.FlagIsExtern {
		scope.Insert(types.NewVar(ctx.goNodePos(decl), ctx.pkg.Types, decl.Name, typ))
	} else {
		if (kind&parser.KindFConst) != 0 && isInteger(typ) && tryNewConstInteger(ctx, typ, decl) {
			return
		}
		newVarAndInit(ctx, scope, typ, decl, global)
		if static != "" {
			substObj(ctx.pkg.Types, ctx.cb.Scope(), static, scope, decl.Name)
		} else if kind == parser.KindFVolatile && !global {
			addr := gox.Lookup(scope, decl.Name)
			ctx.cb.VarRef(nil).Val(addr).Assign(1) // musl: use volatile to mark unused
		}
	}
}

func substObj(pkg *types.Package, scope *types.Scope, static string, scope2 *types.Scope, name string) {
	real := scope2.Lookup(name)
	old := scope.Insert(gox.NewSubst(token.NoPos, pkg, static, real))
	if old != nil {
		if t, ok := old.Type().(*gox.SubstType); ok {
			t.Real = real
		} else {
			log.Panicln(static, "redefined")
		}
	}
}

func avoidKeyword(name *string) {
	switch *name {
	case "map", "type", "range", "chan", "var", "func", "go", "select",
		"defer", "package", "import", "interface", "fallthrough":
		*name += "_"
	}
}

func compileVarWith(ctx *blockCtx, typ types.Type, decl *ast.Node) {
	scope := ctx.cb.Scope()
	newVarAndInit(ctx, scope, typ, decl, false)
}

func compileVarDef(ctx *blockCtx, decl *ast.Node) {
	if debugCompileDecl {
		log.Println("varDef", decl.Name, "-", decl.Loc.PresumedLine)
	}
	typ := toType(ctx, decl.Type, 0)
	avoidKeyword(&decl.Name)
	cb := ctx.cb.DefineVarStart(ctx.goNodePos(decl), decl.Name).Typ(typ)
	if inner := decl.Inner; len(inner) > 0 {
		initExpr := inner[0]
		varInit(ctx, typ, initExpr)
	} else {
		cb.ZeroLit(typ)
	}
	cb.Call(1).EndInit(1)
}

func newVarAndInit(ctx *blockCtx, scope *types.Scope, typ types.Type, decl *ast.Node, global bool) {
	if debugCompileDecl {
		log.Println("var", decl.Name, typ, "-", decl.Kind)
	}
	varDecl, inVBlock := ctx.newVar(scope, ctx.goNodePos(decl), typ, decl.Name)
	inner := decl.Inner
	if len(inner) == 1 && inner[0].Kind == ast.VisibilityAttr {
		inner = inner[1:]
	}
	if len(inner) > 0 {
		initExpr := inner[0]
		if ufs, ok := checkUnion(ctx, typ); ok {
			if inVBlock {
				log.Panicln("TODO: initUnionVar inVBlock")
			}
			initUnionVar(ctx, decl.Name, ufs, initExpr)
			return
		}
		if inVBlock {
			varAssign(ctx, scope, typ, decl.Name, initExpr)
		} else if global && hasFnPtrMember(typ) {
			pkg := ctx.pkg
			cb := pkg.NewFunc(nil, "init", nil, nil, false).BodyStart(pkg)
			varAssign(ctx, scope, typ, decl.Name, initExpr)
			cb.End()
		} else {
			cb := varDecl.InitStart(ctx.pkg)
			varInit(ctx, typ, initExpr)
			cb.EndInit(1)
		}
	} else if inVBlock {
		addr := gox.Lookup(scope, decl.Name)
		ctx.cb.VarRef(addr).ZeroLit(typ).Assign(1)
	}
}

func hasFnPtrMember(typ types.Type) bool {
retry:
	switch t := typ.Underlying().(type) {
	case *types.Struct:
		for i, n := 0, t.NumFields(); i < n; i++ {
			if isFunc(t.Field(i).Type()) {
				return true
			}
		}
	case *types.Array:
		typ = t.Elem()
		goto retry
	}
	return false
}

func varAssign(ctx *blockCtx, scope *types.Scope, typ types.Type, name string, initExpr *ast.Node) {
	addr := gox.Lookup(scope, name)
	cb := ctx.cb.VarRef(addr)
	varInit(ctx, typ, initExpr)
	cb.Assign(1)
}

func varInit(ctx *blockCtx, typ types.Type, initExpr *ast.Node) {
	if initExpr.Kind == ast.InitListExpr {
		initLit(ctx, typ, initExpr)
	} else if !initWithStringLiteral(ctx, typ, initExpr) {
		compileExpr(ctx, initExpr)
	}
}

func initLit(ctx *blockCtx, typ types.Type, initExpr *ast.Node) int {
	switch t := typ.(type) {
	case *types.Array:
		if !initWithStringLiteral(ctx, typ, initExpr) {
			arrayLit(ctx, t, initExpr)
		}
	case *types.Named:
		structLit(ctx, t, initExpr)
	case *bfType:
		if initExpr.Kind != ast.ImplicitValueInitExpr {
			log.Panicln("initLit bfType: TODO")
		}
		if !t.first {
			return 0
		}
		ctx.cb.ZeroLit(t.Type)
	default:
		compileExpr(ctx, initExpr)
	}
	return 1
}

func arrayLit(ctx *blockCtx, t *types.Array, decl *ast.Node) {
	var inits []*ast.Node
	if len(decl.ArrayFiller) > 0 {
		idx := 0
		if decl.ArrayFiller[idx].Kind == ast.ImplicitValueInitExpr {
			idx = 1
		}
		inits = decl.ArrayFiller[idx:]
	} else {
		inits = decl.Inner
	}
	elem := t.Elem()
	for _, initExpr := range inits {
		initLit(ctx, elem, initExpr)
	}
	ctx.cb.ArrayLit(t, len(inits))
}

func structLit(ctx *blockCtx, typ *types.Named, decl *ast.Node) {
	t := ctx.getVStruct(typ)
	n := 0
	for i, initExpr := range decl.Inner {
		n += initLit(ctx, t.Field(i).Type(), initExpr)
	}
	ctx.cb.StructLit(typ, n, false)
}

func checkUnion(ctx *blockCtx, typ types.Type) (ufs *gox.UnionFields, is bool) {
	if t, ok := typ.(*types.Named); ok {
		if vft, ok := ctx.pkg.VFields(t); ok {
			ufs, is = vft.(*gox.UnionFields)
			return
		}
	}
	return nil, false
}

func initUnionVar(ctx *blockCtx, name string, ufs *gox.UnionFields, decl *ast.Node) {
	initExpr := decl.Inner[0]
	t := toType(ctx, initExpr.Type, 0)
	for i, n := 0, ufs.Len(); i < n; i++ {
		fld := ufs.At(i)
		if ctypes.Identical(fld.Type, t) {
			pkg, cb := ctx.pkg, ctx.cb
			scope := cb.Scope()
			obj := gox.Lookup(scope, name)
			global := scope == pkg.Types.Scope()
			if global {
				pkg.NewFunc(nil, "init", nil, nil, false).BodyStart(pkg)
			}
			cb.Val(obj).MemberRef(fld.Name)
			initLit(ctx, t, initExpr)
			cb.Assign(1)
			if global {
				cb.End()
			}
			return
		}
	}
	log.Panicln("initUnion: init with unexpect type -", t)
}

const (
	ncKindInvalid = iota
	ncKindPointer
	ncKindUnsafePointer
	ncKindSignature
)

func checkNilComparable(v *gox.Element) int {
	switch t := v.Type.(type) {
	case *types.Pointer:
		return ncKindPointer
	case *types.Basic:
		switch t.Kind() {
		case types.UnsafePointer:
			return ncKindUnsafePointer
		}
	case *types.Signature:
		return ncKindSignature
	}
	return ncKindInvalid
}

func isNilComparable(typ types.Type) bool {
	switch t := typ.(type) {
	case *types.Pointer, *types.Signature:
		return true
	case *types.Basic:
		if t.Kind() == types.UnsafePointer {
			return true
		}
	}
	return false
}

func isIntegerOrBool(typ types.Type) bool {
	return isKind(typ, types.IsInteger|types.IsBoolean)
}

func isNumber(typ types.Type) bool {
	return isKind(typ, types.IsInteger|types.IsFloat)
}

func isUnsigned(typ types.Type) bool {
	return isKind(typ, types.IsUnsigned)
}

func isInteger(typ types.Type) bool {
	return isKind(typ, types.IsInteger)
}

func isUntyped(typ types.Type) bool {
	return isKind(typ, types.IsUntyped)
}

func isBool(typ types.Type) bool {
	return isKind(typ, types.IsBoolean)
}

func isKind(typ types.Type, mask types.BasicInfo) bool {
	if t, ok := typ.(*types.Basic); ok {
		return (t.Info() & mask) != 0
	}
	return false
}

func isArrayUnknownLen(typ types.Type) bool {
	if t, ok := typ.(*types.Array); ok {
		return t.Len() < 0
	}
	return false
}

func isFunc(typ types.Type) bool {
	_, ok := typ.(*types.Signature)
	return ok
}

// char[N], char[], unsigned char[N], unsigned char[]
func isCharArray(typ types.Type) bool {
	if t, ok := typ.(*types.Array); ok {
		switch t.Elem() {
		case types.Typ[types.Int8], types.Typ[types.Uint8]:
			return true
		}
	}
	return false
}

func initWithStringLiteral(ctx *blockCtx, typ types.Type, decl *ast.Node) bool {
	if isCharArray(typ) {
		switch decl.Kind {
		case ast.InitListExpr:
			inner := decl.Inner
			if len(inner) != 1 || inner[0].Kind != ast.StringLiteral {
				break
			}
			decl = inner[0]
			fallthrough
		case ast.StringLiteral:
			s, err := strconv.Unquote(decl.Value.(string))
			if err != nil {
				log.Panicln("initWithStringLiteral:", err)
			}
			stringLit(ctx.cb, s, typ)
			return true
		}
	}
	return false
}

func tryNewConstInteger(ctx *blockCtx, typ types.Type, decl *ast.Node) bool {
	if len(decl.Inner) > 0 {
		initExpr := decl.Inner[0]
		switch initExpr.Kind {
		case ast.IntegerLiteral:
			cb := ctx.cb.NewConstStart(typ, decl.Name)
			compileExpr(ctx, initExpr)
			cb.EndInit(1)
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
