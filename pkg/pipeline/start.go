package pipeline

import (
	"fmt"
	"github.com/jmoiron/jsonq"
	"github.com/unchain/pipeline/pkg/actions/fileparser_action"
	"github.com/unchain/pipeline/pkg/actions/imap_action"
	"github.com/unchain/pipeline/pkg/actions/templater_action"
	"github.com/unchain/pipeline/pkg/domain"
	"github.com/unchain/pipeline/pkg/triggers/cron_trigger"
	"github.com/unchainio/pkg/errors"
)

func (p *Pipeline) Start() error {
	// Initialize trigger
	trigger := &cron_trigger.Trigger{}
	err := trigger.Init(p.log, []byte(p.cfg.Trigger.Config))
	if err != nil {
		return errors.Wrap(err, "could not init trigger")
	}

	p.log.Debugf("Initialized pipeline trigger")

	go p.start(trigger)

	return nil
}

func (p *Pipeline) start(trigger domain.Trigger) {
	// start infinite loop to process messages
	for {
		select {
		case <-p.stopChannel:
			return
		default:
			tag, _, err := trigger.NextMessage()
			if err != nil {
				p.handleError(trigger, tag, err, 0)
			}
			p.log.Debugf("Next message with tag %v", tag)

			// check email
			imapOutput, err := imap_action.Invoke(p.log, map[string]interface{}{
				imap_action.ConfigInput: p.cfg.Actions.ImapAction.Config,
				imap_action.Function:    "GetNewMessageAttachments",
			})
			if err != nil {
				p.handleError(trigger, tag, err, 0)
			}
			// if no new messages, continue
			if imapOutput == nil {
				err = trigger.Respond(tag, nil, err)
				if err != nil {
					p.handleError(trigger, tag, err, 0)
				}
				continue
			}
			messages, ok := imapOutput["messages"].(map[uint32]interface{})
			if !ok {
				p.handleError(trigger, tag, errors.New("could not cast messages output from imap action"), 0)
			}
			p.log.Debugf("New messages: # %v", len(messages))

			// handle email messages
			seqNum, err := p.handleMessages(messages)
			if err != nil {
				p.handleError(trigger, tag, err, seqNum)
			}

			// call respond to finish processing
			err = trigger.Respond(tag, nil, err)
			if err != nil {
				p.handleError(trigger, tag, err, 0)
			}
		}
	}
}

// handle email messages in loop
func (p *Pipeline) handleMessages(messages map[uint32]interface{}) (uint32, error) {
	fmt.Printf("GOT #%v MESSAGES\n", len(messages))
	for seqNum, message := range messages {
		// file parsing
		fileparserOutput, err := fileparser_action.Invoke(p.log, map[string]interface{}{
			fileparser_action.FileType:  p.cfg.Actions.FileparserAction.Filetype,
			fileparser_action.File:      message,
			fileparser_action.Header:    p.cfg.Actions.FileparserAction.Header,
			fileparser_action.Delimiter: ';',
		})
		if err != nil {
			return seqNum, errors.Wrapf(err, "could not parse file in email with seqNum %v\n", seqNum)
		}
		p.log.Debugf("Parsed file, output: %v", fileparserOutput)
		// data transformation
		records, ok := fileparserOutput["messages"].([]map[string]interface{})
		if !ok {
			return seqNum, errors.Errorf("could not cast fileparser messages output for mail with seqNum: %v\n", seqNum)
		}

		err = p.handleRecords(records)
		if err != nil {
			return seqNum, errors.Wrapf(err, "error in email with seqNum %v - record handling stopped at index prior to error index\n", seqNum)
		}

		fmt.Println("will mark messages as read")
		// mark message as read
		_, err = imap_action.Invoke(p.log, map[string]interface{}{
			imap_action.ConfigInput: p.cfg.Actions.ImapAction.Config,
			imap_action.Function:    "MarkMessageAsRead",
			imap_action.Params: map[string]interface{}{
				"seqNum": int(seqNum),
			},
		})
		if err != nil {
			return seqNum, errors.Wrapf(err, "error in email with seqNum %v\n", seqNum)
		}
	}

	return 0, nil
}

// handle product batch records in loop
func (p *Pipeline) handleRecords(records []map[string]interface{}) error {
	fmt.Printf("GOT #%v RECORDS\n", len(records))
	for index, record := range records {
		inputVariables := GetInputVariables(jsonq.NewQuery(record), p.cfg.Actions.TemplaterAction.Variables)
		templaterOutput, err := templater_action.Invoke(p.log, map[string]interface{}{
			templater_action.InputTemplate:  p.cfg.Actions.TemplaterAction.Template,
			templater_action.InputVariables: inputVariables,
		})
		if err != nil {
			return errors.Wrapf(err, "could not transform data for record with index %v", index)
		}
		fmt.Println("HTTP INPUT ")
		fmt.Println(fmt.Sprintf("%s", templaterOutput[templater_action.TemplateResult]))

		// call import-api
		// httpOutput, err := http_action.Invoke(p.log, map[string]interface{}{
		// 	http_action.RequestBody: []byte(fmt.Sprintf("%s", templaterOutput[templater_action.TemplateResult])),
		// 	http_action.Url:         p.cfg.Actions.HttpAction.Url,
		// 	http_action.Method:      p.cfg.Actions.HttpAction.Method,
		// })
		// if err != nil {
		// 	return errors.Wrapf(err, "could not call import-api for record with ID %v \n HTTP response: %v", index, httpOutput)
		// }
		// FIXME why is this not proceeding??
		fmt.Println("done sending request")
	}

	return nil
}