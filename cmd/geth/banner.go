// Copyright 2026 The Rogue Signal Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
)

const rogueBanner = `
                                         ██████                                         
                               ██████████████████████████                               
                          █████████                   ████████                          
                       █████                                █████                       
                    ████                                        ████                    
                 ████                                              ████                 
               ████        █                                █        ████               
             ███         ████                              █████        ███             
           ████       ██████                                ██████       ████           
         ████        █████     █                        █     █████        ████         
        ███        █████     █████         ██          █████    █████        ███        
       ███        █████    █████          ████          █████    █████        ███       
      ███         ████    ████    ███    ██████    ███    ████    ████         ███      
     ███         ████    ████   █████    ███████   █████   ████    ████          ██     
    ██           ███    ████    ████   ██████████   █████   ████    ███           ██    
   ███          ████    ████   ███    ████    ████    ███   ████    ████          ███   
  ███           ████    ████   ███    ████    ████    ████   ███    ████           ███  
  ██            ████   █████   ███     ██████████     ████   ████   ████            ██  
 ███            ████    ████   ███       ██████       ███   ████    ████            ███ 
 ███            ████    ████   ████      ██████      ████   ████    ████            ███ 
███              ███    █████   █████   ████████   █████   █████   ████              ███
███              ████     ████    ███   ███  ████  ███    ████     ████              ███
██                ████     ████        ███    ████      █████     ████                ██
██                 █████    █████     ████████████     █████     ████                 ██
██                  █████     ██     ██████████████     ██     █████                  ██
██                    █████          █████    ██████         ██████                   ██
███                    ██████       █████      █████       ██████                     ██
███                      ███       ██████████████████       ███                      ███
 ██                                ███   ██████   ████                               ██ 
 ███                              █████████  ██████████                             ███ 
  ██                             ████████      ████████                             ███ 
  ███                           ████   ███████████  ████                           ███  
   ███                          ████   ██████████    ████                         ███   
    ██                         ██████████      ██████ ███                         ██    
     ███                      ████████             ███████                       ███    
      ███                    ███████████         ██████████                    ████     
       ███      ██           ████    ██████  ██████    █████           █      ███       
        ███      ████       ████        ████████        ████        ███      ███        
          ███    ██████    ████      ███████████████     ████    ██████    ████         
           ████   ████     ████  ███████        ███████   ████    ████   ████           
             ████   ███   ██████████                ██████████   ███   ████             
               █████   █ ███████████                ███████████ █   █████               
                  ████  █████   ████████        ████████   █████  █████                 
                    ████████         ██████████████         ████████                    
                       ██████           ████████           ██████                       
                           ██████████████████████████████████                           
                               ██████████████████████████                               
                                       █████████                                        
           ROGUE SIGNAL  |  Chain ID: 800055  |  Consensus: RandomX (rx/0)
`

// printRogueBanner writes the Rogue Signal ASCII art banner to stderr when
// running in interactive or standard terminal logging mode.
func printRogueBanner(ctx *cli.Context) {
	if ctx.IsSet("verbosity") && ctx.Int("verbosity") <= 0 {
		return
	}
	format := strings.ToLower(ctx.String("log.format"))
	if ctx.Bool("log.json") || format == "json" || format == "logfmt" {
		return
	}

	if fi, err := os.Stderr.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		fmt.Fprintf(os.Stderr, "\033[36m%s\033[0m", rogueBanner)
		return
	}
	fmt.Fprint(os.Stderr, rogueBanner)
}
