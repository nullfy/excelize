// Copyright 2016 - 2020 The excelize Authors. All rights reserved. Use of
// this source code is governed by a BSD-style license that can be found in
// the LICENSE file.
//
// Package excelize providing a set of functions that allow you to write to
// and read from XLSX / XLSM / XLTM files. Supports reading and writing
// spreadsheet documents generated by Microsoft Exce™ 2007 and later. Supports
// complex components by high compatibility, and provided streaming API for
// generating or reading data from a worksheet with huge amounts of data. This
// library needs Go version 1.10 or later.

package excelize

import (
	"container/list"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/xuri/efp"
)

// Excel formula errors
const (
	formulaErrorDIV         = "#DIV/0!"
	formulaErrorNAME        = "#NAME?"
	formulaErrorNA          = "#N/A"
	formulaErrorNUM         = "#NUM!"
	formulaErrorVALUE       = "#VALUE!"
	formulaErrorREF         = "#REF!"
	formulaErrorNULL        = "#NULL"
	formulaErrorSPILL       = "#SPILL!"
	formulaErrorCALC        = "#CALC!"
	formulaErrorGETTINGDATA = "#GETTING_DATA"
)

// cellRef defines the structure of a cell reference
type cellRef struct {
	Col   int
	Row   int
	Sheet string
}

// cellRef defines the structure of a cell range
type cellRange struct {
	From cellRef
	To   cellRef
}

type formulaFuncs struct{}

// CalcCellValue provides a function to get calculated cell value. This
// feature is currently in beta. Array formula, table formula and some other
// formulas are not supported currently.
func (f *File) CalcCellValue(sheet, cell string) (result string, err error) {
	var (
		formula string
		token   efp.Token
	)
	if formula, err = f.GetCellFormula(sheet, cell); err != nil {
		return
	}
	ps := efp.ExcelParser()
	tokens := ps.Parse(formula)
	if tokens == nil {
		return
	}
	if token, err = f.evalInfixExp(sheet, tokens); err != nil {
		return
	}
	result = token.TValue
	return
}

// getPriority calculate arithmetic operator priority.
func getPriority(token efp.Token) (pri int) {
	var priority = map[string]int{
		"*": 2,
		"/": 2,
		"+": 1,
		"-": 1,
	}
	pri, _ = priority[token.TValue]
	if token.TValue == "-" && token.TType == efp.TokenTypeOperatorPrefix {
		pri = 3
	}
	if token.TSubType == efp.TokenSubTypeStart && token.TType == efp.TokenTypeSubexpression { // (
		pri = 0
	}
	return
}

// evalInfixExp evaluate syntax analysis by given infix expression after
// lexical analysis. Evaluate an infix expression containing formulas by
// stacks:
//
//    opd  - Operand
//    opt  - Operator
//    opf  - Operation formula
//    opfd - Operand of the operation formula
//    opft - Operator of the operation formula
//    args - Arguments of the operation formula
//
func (f *File) evalInfixExp(sheet string, tokens []efp.Token) (efp.Token, error) {
	var err error
	opdStack, optStack, opfStack, opfdStack, opftStack, argsStack := NewStack(), NewStack(), NewStack(), NewStack(), NewStack(), NewStack()
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]

		// out of function stack
		if opfStack.Len() == 0 {
			if err = f.parseToken(sheet, token, opdStack, optStack); err != nil {
				return efp.Token{}, err
			}
		}

		// function start
		if token.TType == efp.TokenTypeFunction && token.TSubType == efp.TokenSubTypeStart {
			opfStack.Push(token)
			continue
		}

		// in function stack, walk 2 token at once
		if opfStack.Len() > 0 {
			var nextToken efp.Token
			if i+1 < len(tokens) {
				nextToken = tokens[i+1]
			}

			// current token is args or range, skip next token, order required: parse reference first
			if token.TSubType == efp.TokenSubTypeRange {
				if !opftStack.Empty() {
					// parse reference: must reference at here
					result, err := f.parseReference(sheet, token.TValue)
					if err != nil {
						return efp.Token{TValue: formulaErrorNAME}, err
					}
					if len(result) != 1 {
						return efp.Token{}, errors.New(formulaErrorVALUE)
					}
					opfdStack.Push(efp.Token{
						TType:    efp.TokenTypeOperand,
						TSubType: efp.TokenSubTypeNumber,
						TValue:   result[0],
					})
					continue
				}
				if nextToken.TType == efp.TokenTypeArgument || nextToken.TType == efp.TokenTypeFunction {
					// parse reference: reference or range at here
					result, err := f.parseReference(sheet, token.TValue)
					if err != nil {
						return efp.Token{TValue: formulaErrorNAME}, err
					}
					for _, val := range result {
						argsStack.Push(efp.Token{
							TType:    efp.TokenTypeOperand,
							TSubType: efp.TokenSubTypeNumber,
							TValue:   val,
						})
					}
					if len(result) == 0 {
						return efp.Token{}, errors.New(formulaErrorVALUE)
					}
					continue
				}
			}

			// check current token is opft
			if err = f.parseToken(sheet, token, opfdStack, opftStack); err != nil {
				return efp.Token{}, err
			}

			// current token is arg
			if token.TType == efp.TokenTypeArgument {
				for !opftStack.Empty() {
					// calculate trigger
					topOpt := opftStack.Peek().(efp.Token)
					if err := calculate(opfdStack, topOpt); err != nil {
						return efp.Token{}, err
					}
					opftStack.Pop()
				}
				if !opfdStack.Empty() {
					argsStack.Push(opfdStack.Pop())
				}
				continue
			}

			// current token is function stop
			if token.TType == efp.TokenTypeFunction && token.TSubType == efp.TokenSubTypeStop {
				for !opftStack.Empty() {
					// calculate trigger
					topOpt := opftStack.Peek().(efp.Token)
					if err := calculate(opfdStack, topOpt); err != nil {
						return efp.Token{}, err
					}
					opftStack.Pop()
				}

				// push opfd to args
				if opfdStack.Len() > 0 {
					argsStack.Push(opfdStack.Pop())
				}
				// call formula function to evaluate
				result, err := callFuncByName(&formulaFuncs{}, opfStack.Peek().(efp.Token).TValue, []reflect.Value{reflect.ValueOf(argsStack)})
				if err != nil {
					return efp.Token{}, err
				}
				opfStack.Pop()
				if opfStack.Len() > 0 { // still in function stack
					opfdStack.Push(efp.Token{TValue: result, TType: efp.TokenTypeOperand, TSubType: efp.TokenSubTypeNumber})
				} else {
					opdStack.Push(efp.Token{TValue: result, TType: efp.TokenTypeOperand, TSubType: efp.TokenSubTypeNumber})
				}
			}
		}
	}
	for optStack.Len() != 0 {
		topOpt := optStack.Peek().(efp.Token)
		if err = calculate(opdStack, topOpt); err != nil {
			return efp.Token{}, err
		}
		optStack.Pop()
	}
	return opdStack.Peek().(efp.Token), err
}

// calculate evaluate basic arithmetic operations.
func calculate(opdStack *Stack, opt efp.Token) error {
	if opt.TValue == "-" && opt.TType == efp.TokenTypeOperatorPrefix {
		opd := opdStack.Pop().(efp.Token)
		opdVal, err := strconv.ParseFloat(opd.TValue, 64)
		if err != nil {
			return err
		}
		result := 0 - opdVal
		opdStack.Push(efp.Token{TValue: fmt.Sprintf("%g", result), TType: efp.TokenTypeOperand, TSubType: efp.TokenSubTypeNumber})
	}
	if opt.TValue == "+" {
		rOpd := opdStack.Pop().(efp.Token)
		lOpd := opdStack.Pop().(efp.Token)
		lOpdVal, err := strconv.ParseFloat(lOpd.TValue, 64)
		if err != nil {
			return err
		}
		rOpdVal, err := strconv.ParseFloat(rOpd.TValue, 64)
		if err != nil {
			return err
		}
		result := lOpdVal + rOpdVal
		opdStack.Push(efp.Token{TValue: fmt.Sprintf("%g", result), TType: efp.TokenTypeOperand, TSubType: efp.TokenSubTypeNumber})
	}
	if opt.TValue == "-" && opt.TType == efp.TokenTypeOperatorInfix {
		rOpd := opdStack.Pop().(efp.Token)
		lOpd := opdStack.Pop().(efp.Token)
		lOpdVal, err := strconv.ParseFloat(lOpd.TValue, 64)
		if err != nil {
			return err
		}
		rOpdVal, err := strconv.ParseFloat(rOpd.TValue, 64)
		if err != nil {
			return err
		}
		result := lOpdVal - rOpdVal
		opdStack.Push(efp.Token{TValue: fmt.Sprintf("%g", result), TType: efp.TokenTypeOperand, TSubType: efp.TokenSubTypeNumber})
	}
	if opt.TValue == "*" {
		rOpd := opdStack.Pop().(efp.Token)
		lOpd := opdStack.Pop().(efp.Token)
		lOpdVal, err := strconv.ParseFloat(lOpd.TValue, 64)
		if err != nil {
			return err
		}
		rOpdVal, err := strconv.ParseFloat(rOpd.TValue, 64)
		if err != nil {
			return err
		}
		result := lOpdVal * rOpdVal
		opdStack.Push(efp.Token{TValue: fmt.Sprintf("%g", result), TType: efp.TokenTypeOperand, TSubType: efp.TokenSubTypeNumber})
	}
	if opt.TValue == "/" {
		rOpd := opdStack.Pop().(efp.Token)
		lOpd := opdStack.Pop().(efp.Token)
		lOpdVal, err := strconv.ParseFloat(lOpd.TValue, 64)
		if err != nil {
			return err
		}
		rOpdVal, err := strconv.ParseFloat(rOpd.TValue, 64)
		if err != nil {
			return err
		}
		result := lOpdVal / rOpdVal
		if rOpdVal == 0 {
			return errors.New(formulaErrorDIV)
		}
		opdStack.Push(efp.Token{TValue: fmt.Sprintf("%g", result), TType: efp.TokenTypeOperand, TSubType: efp.TokenSubTypeNumber})
	}
	return nil
}

// parseToken parse basic arithmetic operator priority and evaluate based on
// operators and operands.
func (f *File) parseToken(sheet string, token efp.Token, opdStack, optStack *Stack) error {
	// parse reference: must reference at here
	if token.TSubType == efp.TokenSubTypeRange {
		result, err := f.parseReference(sheet, token.TValue)
		if err != nil {
			return errors.New(formulaErrorNAME)
		}
		if len(result) != 1 {
			return errors.New(formulaErrorVALUE)
		}
		token.TValue = result[0]
		token.TType = efp.TokenTypeOperand
		token.TSubType = efp.TokenSubTypeNumber
	}
	if (token.TValue == "-" && token.TType == efp.TokenTypeOperatorPrefix) || token.TValue == "+" || token.TValue == "-" || token.TValue == "*" || token.TValue == "/" {
		if optStack.Len() == 0 {
			optStack.Push(token)
		} else {
			tokenPriority := getPriority(token)
			topOpt := optStack.Peek().(efp.Token)
			topOptPriority := getPriority(topOpt)
			if tokenPriority > topOptPriority {
				optStack.Push(token)
			} else {
				for tokenPriority <= topOptPriority {
					optStack.Pop()
					if err := calculate(opdStack, topOpt); err != nil {
						return err
					}
					if optStack.Len() > 0 {
						topOpt = optStack.Peek().(efp.Token)
						topOptPriority = getPriority(topOpt)
						continue
					}
					break
				}
				optStack.Push(token)
			}
		}
	}
	if token.TType == efp.TokenTypeSubexpression && token.TSubType == efp.TokenSubTypeStart { // (
		optStack.Push(token)
	}
	if token.TType == efp.TokenTypeSubexpression && token.TSubType == efp.TokenSubTypeStop { // )
		for optStack.Peek().(efp.Token).TSubType != efp.TokenSubTypeStart && optStack.Peek().(efp.Token).TType != efp.TokenTypeSubexpression { // != (
			topOpt := optStack.Peek().(efp.Token)
			if err := calculate(opdStack, topOpt); err != nil {
				return err
			}
			optStack.Pop()
		}
		optStack.Pop()
	}
	// opd
	if token.TType == efp.TokenTypeOperand && token.TSubType == efp.TokenSubTypeNumber {
		opdStack.Push(token)
	}
	return nil
}

// parseReference parse reference and extract values by given reference
// characters and default sheet name.
func (f *File) parseReference(sheet, reference string) (result []string, err error) {
	reference = strings.Replace(reference, "$", "", -1)
	refs, cellRanges, cellRefs := list.New(), list.New(), list.New()
	for _, ref := range strings.Split(reference, ":") {
		tokens := strings.Split(ref, "!")
		cr := cellRef{}
		if len(tokens) == 2 { // have a worksheet name
			cr.Sheet = tokens[0]
			if cr.Col, cr.Row, err = CellNameToCoordinates(tokens[1]); err != nil {
				return
			}
			if refs.Len() > 0 {
				e := refs.Back()
				cellRefs.PushBack(e.Value.(cellRef))
				refs.Remove(e)
			}
			refs.PushBack(cr)
			continue
		}
		if cr.Col, cr.Row, err = CellNameToCoordinates(tokens[0]); err != nil {
			return
		}
		e := refs.Back()
		if e == nil {
			cr.Sheet = sheet
			refs.PushBack(cr)
			continue
		}
		cellRanges.PushBack(cellRange{
			From: e.Value.(cellRef),
			To:   cr,
		})
		refs.Remove(e)
	}
	if refs.Len() > 0 {
		e := refs.Back()
		cellRefs.PushBack(e.Value.(cellRef))
		refs.Remove(e)
	}

	result, err = f.rangeResolver(cellRefs, cellRanges)
	return
}

// rangeResolver extract value as string from given reference and range list.
// This function will not ignore the empty cell. Note that the result of 3D
// range references may be different from Excel in some cases, for example,
// A1:A2:A2:B3 in Excel will include B2, but we wont.
func (f *File) rangeResolver(cellRefs, cellRanges *list.List) (result []string, err error) {
	filter := map[string]string{}
	// extract value from ranges
	for temp := cellRanges.Front(); temp != nil; temp = temp.Next() {
		cr := temp.Value.(cellRange)
		if cr.From.Sheet != cr.To.Sheet {
			err = errors.New(formulaErrorVALUE)
		}
		rng := []int{cr.From.Col, cr.From.Row, cr.To.Col, cr.To.Row}
		sortCoordinates(rng)
		for col := rng[0]; col <= rng[2]; col++ {
			for row := rng[1]; row <= rng[3]; row++ {
				var cell string
				if cell, err = CoordinatesToCellName(col, row); err != nil {
					return
				}
				if filter[cell], err = f.GetCellValue(cr.From.Sheet, cell); err != nil {
					return
				}
			}
		}
	}
	// extract value from references
	for temp := cellRefs.Front(); temp != nil; temp = temp.Next() {
		cr := temp.Value.(cellRef)
		var cell string
		if cell, err = CoordinatesToCellName(cr.Col, cr.Row); err != nil {
			return
		}
		if filter[cell], err = f.GetCellValue(cr.Sheet, cell); err != nil {
			return
		}
	}

	for _, val := range filter {
		result = append(result, val)
	}
	return
}

// callFuncByName calls the no error or only error return function with
// reflect by given receiver, name and parameters.
func callFuncByName(receiver interface{}, name string, params []reflect.Value) (result string, err error) {
	function := reflect.ValueOf(receiver).MethodByName(name)
	if function.IsValid() {
		rt := function.Call(params)
		if len(rt) == 0 {
			return
		}
		if !rt[1].IsNil() {
			err = rt[1].Interface().(error)
			return
		}
		result = rt[0].Interface().(string)
		return
	}
	err = fmt.Errorf("not support %s function", name)
	return
}

// Math and Trigonometric functions

// SUM function adds together a supplied set of numbers and returns the sum of
// these values. The syntax of the function is:
//
//    SUM(number1,[number2],...)
//
func (fn *formulaFuncs) SUM(argsStack *Stack) (result string, err error) {
	var val float64
	var sum float64
	for !argsStack.Empty() {
		token := argsStack.Pop().(efp.Token)
		if token.TValue == "" {
			continue
		}
		val, err = strconv.ParseFloat(token.TValue, 64)
		if err != nil {
			return
		}
		sum += val
	}
	result = fmt.Sprintf("%g", sum)
	return
}

// PRODUCT function returns the product (multiplication) of a supplied set of numerical values.
// The syntax of the function is:
//
//    PRODUCT(number1,[number2],...)
//
func (fn *formulaFuncs) PRODUCT(argsStack *Stack) (result string, err error) {
	var (
		val     float64
		product float64 = 1
	)
	for !argsStack.Empty() {
		token := argsStack.Pop().(efp.Token)
		if token.TValue == "" {
			continue
		}
		val, err = strconv.ParseFloat(token.TValue, 64)
		if err != nil {
			return
		}
		product = product * val
	}
	result = fmt.Sprintf("%g", product)
	return
}

// PRODUCT function calculates a given number, raised to a supplied power.
// The syntax of the function is:
//
//    POWER(number,power)
//
func (fn *formulaFuncs) POWER(argsStack *Stack) (result string, err error) {
	if argsStack.Len() != 2 {
		err = errors.New("POWER requires 2 numeric arguments")
		return
	}
	var x, y float64
	y, err = strconv.ParseFloat(argsStack.Pop().(efp.Token).TValue, 64)
	if err != nil {
		return
	}
	x, err = strconv.ParseFloat(argsStack.Pop().(efp.Token).TValue, 64)
	if err != nil {
		return
	}
	if x == 0 && y == 0 {
		err = errors.New(formulaErrorNUM)
		return
	}
	if x == 0 && y < 0 {
		err = errors.New(formulaErrorDIV)
		return
	}
	result = fmt.Sprintf("%g", math.Pow(x, y))
	return
}

// SQRT function calculates the positive square root of a supplied number.
// The syntax of the function is:
//
//    SQRT(number)
//
func (fn *formulaFuncs) SQRT(argsStack *Stack) (result string, err error) {
	if argsStack.Len() != 1 {
		err = errors.New("SQRT requires 1 numeric arguments")
		return
	}
	var val float64
	val, err = strconv.ParseFloat(argsStack.Pop().(efp.Token).TValue, 64)
	if err != nil {
		return
	}
	if val < 0 {
		err = errors.New(formulaErrorNUM)
		return
	}
	result = fmt.Sprintf("%g", math.Sqrt(val))
	return
}

// QUOTIENT function returns the integer portion of a division between two supplied numbers.
// The syntax of the function is:
//
//   QUOTIENT(numerator,denominator)
//
func (fn *formulaFuncs) QUOTIENT(argsStack *Stack) (result string, err error) {
	if argsStack.Len() != 2 {
		err = errors.New("QUOTIENT requires 2 numeric arguments")
		return
	}
	var x, y float64
	y, err = strconv.ParseFloat(argsStack.Pop().(efp.Token).TValue, 64)
	if err != nil {
		return
	}
	x, err = strconv.ParseFloat(argsStack.Pop().(efp.Token).TValue, 64)
	if err != nil {
		return
	}
	if y == 0 {
		err = errors.New(formulaErrorDIV)
		return
	}
	result = fmt.Sprintf("%g", math.Trunc(x/y))
	return
}
