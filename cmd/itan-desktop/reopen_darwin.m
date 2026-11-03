#import <Cocoa/Cocoa.h>

extern void itanReopened(void);

@interface ItanReopenHandler : NSObject
@end

@implementation ItanReopenHandler
- (void)handleReopen:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
  itanReopened();
}
@end

static ItanReopenHandler *handler;

// itanWatchReopen asks for the "reopen" Apple event, sent when the app is
// opened again while it runs.
void itanWatchReopen(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    handler = [ItanReopenHandler new];
    [[NSAppleEventManager sharedAppleEventManager] setEventHandler:handler
                                                       andSelector:@selector(handleReopen:withReplyEvent:)
                                                     forEventClass:kCoreEventClass
                                                        andEventID:kAEReopenApplication];
  });
}
