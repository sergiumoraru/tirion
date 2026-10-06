**free
Ctl-Opt DFTACTGRP(*NO) ACTGRP(*NEW);

Dcl-F QCUSTCDT Usage(*Update:*Delete:*Output) USROPN EXTDESC('QIWS/QCUSTCDT') RECNO(RRN);

Dcl-PR QCmdexc EXTPGM('QCMDEXC');
  Cmd  Char(512);
  Lgth Packed(15:5) CONST;
End-PR;

Dcl-S RRN Packed(10:0) INZ(10);
Dcl-S Reply Char(1) INZ('*');
Dcl-S Cmd Char(512);

*inlr = *on;
Cmd = 'OVRDBF FILE(QCUSTCDT) WAITRCD(1)';
QCmdexc(Cmd: 512);
chain(e) RRN QCUSTCDT;
if %error;
  exsr Prog_Cancelled;
endif;
close QCUSTCDT;
return;

begsr Prog_Cancelled;
  dsply 'Cancelled' '' Reply;
  return;
endsr;
